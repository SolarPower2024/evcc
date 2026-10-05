package logstash

// Custom extension: the log in daily files, set in the ui on the log page.
//
// evcc keeps its log in a ring buffer for the log page, a few hours of debug.
// This writes the same stream (all areas and levels, already redacted) to
// <dir>/evcc-YYYY-MM-DD.log from a level of its own, independent of the console
// level. The first line of a new day switches the file, the day before is
// compressed in the background, older files are deleted by age and by a fixed
// size limit for the whole folder.
//
// Output sits between the loggers and the ring buffer. It hands every line
// to the buffer unchanged and, under the buffer's lock, to the file. Switching
// the file on dumps the buffer into it under the same lock, so there is neither
// a gap nor a line twice. Nothing is written, and no folder created, until
// SetFile switches it on.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	jww "github.com/spf13/jwalterweatherman"
)

const (
	fileSizeLimit  = 1 << 30 // all files together
	fileBufferSize = 64 << 10
	fileFlushEvery = 2 * time.Second
	fileMinDays    = 1
	fileMaxDays    = 90

	dayLayout  = "2006-01-02"
	timeLayout = "2006/01/02 15:04:05" // as the loggers write it
)

// fileNameRE matches the files this writes, their date is the first group
var fileNameRE = regexp.MustCompile(`^evcc-(\d{4}-\d{2}-\d{2})\.log(\.gz)?$`)

// maintainMu lets one folder clean-up run at a time, also across a switched off and on file
var maintainMu sync.Mutex

// FileConfig is the log file setting
type FileConfig struct {
	Enabled bool   `json:"enabled"`
	Level   string `json:"level"` // error, warn, info, debug or trace
	Days    int    `json:"days"`  // retention

	// set by the caller, not part of the stored or posted setting
	Dir     string                      `json:"-"` // folder of the files
	Version string                      `json:"-"` // evcc version for the header line
	Log     func(jww.Threshold, string) `json:"-"` // reports warnings and errors
}

// DefaultFileConfig is what the ui starts with
var DefaultFileConfig = FileConfig{Level: "debug", Days: 14}

// FileState is what the ui shows
type FileState struct {
	Enabled bool   `json:"enabled"`
	Level   string `json:"level"`
	Days    int    `json:"days"`
	Dir     string `json:"dir"`
	Files   int    `json:"files"`
	Size    int64  `json:"size"`
	Error   string `json:"error,omitempty"`
}

// Validate checks level and retention
func (c FileConfig) Validate() error {
	switch strings.ToLower(c.Level) {
	case "error", "warn", "info", "debug", "trace":
	default:
		return fmt.Errorf("invalid log level: %q", c.Level)
	}

	if c.Days < fileMinDays || c.Days > fileMaxDays {
		return fmt.Errorf("invalid retention: %d days, allowed %d to %d", c.Days, fileMinDays, fileMaxDays)
	}

	return nil
}

// Output is where the loggers write to: the log buffer of the log page and, if switched on, the file
var Output io.Writer = out

var out = newTee(DefaultHandler)

// SetFile switches the log file on, off or to other settings. The error is one
// of the settings, a file that cannot be written is in the state.
func SetFile(cfg FileConfig) (FileState, error) {
	return out.setFile(cfg)
}

// FileStatus returns folder, files, size and the error that switched the file off
func FileStatus() FileState {
	return out.status()
}

// CloseFile writes out what is buffered and closes the file, on shutdown
func CloseFile() {
	out.close()
}

// tee hands every line to the log buffer and to the file
type tee struct {
	l *logger

	// for tests
	now        func() time.Time
	sizeLimit  int64
	flushEvery time.Duration

	// guarded by l.mu
	cfg  FileConfig
	sink *fileSink
	err  error // why the file is off, kept until the next setting
}

var _ io.Writer = (*tee)(nil)

func newTee(l *logger) *tee {
	return &tee{l: l, now: time.Now, sizeLimit: fileSizeLimit, flushEvery: fileFlushEvery}
}

func (t *tee) Write(p []byte) (int, error) {
	return t.l.writeWith(p, t.toFile)
}

// toFile runs under the lock of the log buffer
func (t *tee) toFile(e element) {
	if t.sink == nil {
		return
	}

	t.sink.write(e)

	if err := t.sink.failure(); err != nil {
		t.fail(err)
	}
}

// fail switches the file off for this run. The setting stays, the next start tries again.
func (t *tee) fail(err error) {
	t.sink.close()
	t.sink = nil
	t.err = err
	t.report(jww.LevelError, fmt.Sprintf("log file switched off: %v", err))
}

// report logs asynchronously: the caller holds the lock of the log buffer, which the log line needs
func (t *tee) report(level jww.Threshold, msg string) {
	if log := t.cfg.Log; log != nil {
		go log(level, msg)
	}
}

func (t *tee) setFile(cfg FileConfig) (FileState, error) {
	if err := cfg.Validate(); err != nil {
		return t.status(), err
	}

	t.l.mu.Lock()

	if t.sink != nil {
		t.sink.close()
		t.sink = nil
	}

	t.cfg = cfg
	t.err = nil

	if cfg.Enabled {
		s, err := newFileSink(cfg, t.now, t.sizeLimit, t.flushEvery)
		if err == nil {
			// the buffer first, then the lines that follow, nothing in between
			t.l.merged(func(e entry) { s.write(e.text) })
			s.flush() // the folder shows the file as it is switched on
			err = s.failure()
		}

		if err != nil {
			if s != nil {
				s.close()
			}
			t.err = err
			t.report(jww.LevelError, fmt.Sprintf("log file not written: %v", err))
		} else {
			s.start()
			s.maintainAsync()
			t.sink = s
		}
	}

	t.l.mu.Unlock()

	return t.status(), nil
}

func (t *tee) status() FileState {
	t.l.mu.RLock()
	cfg, sink, err := t.cfg, t.sink, t.err
	t.l.mu.RUnlock()

	if cfg.Level == "" {
		cfg.Level = DefaultFileConfig.Level
	}
	if cfg.Days == 0 {
		cfg.Days = DefaultFileConfig.Days
	}

	if err == nil && sink != nil {
		err = sink.failure()
	}

	res := FileState{Enabled: cfg.Enabled, Level: strings.ToLower(cfg.Level), Days: cfg.Days, Dir: cfg.Dir}
	if err != nil {
		res.Error = err.Error()
	}

	if cfg.Dir != "" {
		for _, f := range listFiles(cfg.Dir) {
			res.Files++
			res.Size += f.size
		}
	}

	return res
}

func (t *tee) close() {
	t.l.mu.Lock()
	defer t.l.mu.Unlock()

	if t.sink != nil {
		t.sink.close()
		t.sink = nil
	}
}

// writeWith is Write, additionally handing the stored line to fn while the lock is held
func (l *logger) writeWith(p []byte, fn func(element)) (int, error) {
	if bytes.HasPrefix(p, []byte("[cache ]")) {
		return len(p), nil
	}

	e := element(p)
	_, level := e.areaLevel()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.seq++
	if level == jww.LevelTrace {
		l.trace.add(entry{l.seq, e})
	} else {
		l.other.add(entry{l.seq, e})
	}

	fn(e)

	return len(p), nil
}

// merged calls fn for every stored entry in the order it was written. The caller holds the lock.
func (l *logger) merged(fn func(entry)) {
	var trace, other []entry
	l.trace.visit(func(e entry) { trace = append(trace, e) })
	l.other.visit(func(e entry) { other = append(other, e) })

	for len(trace) > 0 || len(other) > 0 {
		if len(other) == 0 || (len(trace) > 0 && trace[0].seq < other[0].seq) {
			fn(trace[0])
			trace = trace[1:]
		} else {
			fn(other[0])
			other = other[1:]
		}
	}
}

// fileSink writes the lines of one day to a file and switches at midnight
type fileSink struct {
	dir, version string
	level        jww.Threshold
	levelName    string
	days         int
	sizeLimit    int64
	now          func() time.Time
	log          func(jww.Threshold, string)

	mu  sync.Mutex
	f   *os.File
	w   *bufio.Writer
	day string
	err error

	flushEvery time.Duration
	stopC      chan struct{}
	ticker     sync.WaitGroup
	bg         sync.WaitGroup // clean-up runs
}

// newFileSink creates the folder and opens today's file with its header line
func newFileSink(cfg FileConfig, now func() time.Time, sizeLimit int64, flushEvery time.Duration) (*fileSink, error) {
	if cfg.Dir == "" {
		return nil, errors.New("no folder for the log files")
	}

	s := &fileSink{
		dir:        cfg.Dir,
		version:    cfg.Version,
		level:      LogLevelToThreshold(cfg.Level),
		levelName:  strings.ToLower(cfg.Level),
		days:       cfg.Days,
		sizeLimit:  sizeLimit,
		now:        now,
		log:        cfg.Log,
		flushEvery: flushEvery,
		stopC:      make(chan struct{}),
	}

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.open(); err != nil {
		return nil, err
	}

	return s, nil
}

func fileName(day string) string {
	return "evcc-" + day + ".log"
}

// open opens the file of the current day and writes the header line, the caller holds s.mu
func (s *fileSink) open() error {
	now := s.now()
	day := now.Format(dayLayout)

	f, err := os.OpenFile(filepath.Join(s.dir, fileName(day)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		s.err = err
		return err
	}

	s.f, s.w, s.day = f, bufio.NewWriterSize(f, fileBufferSize), day
	s.line(fmt.Sprintf("[logfil] INFO %s evcc %s, level %s", now.Format(timeLayout), s.version, s.levelName))

	return s.err
}

// line writes a line to the buffer, the caller holds s.mu
func (s *fileSink) line(text string) {
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}

	if _, err := s.w.WriteString(text); err != nil {
		s.fail(err)
	}
}

// fail switches this file off, the caller holds s.mu
func (s *fileSink) fail(err error) {
	if s.err == nil {
		s.err = err
	}
	if s.f != nil {
		_ = s.f.Close()
	}
	s.f, s.w = nil, nil
}

// write adds a line from the level on, the first line of a new day switches the file
func (s *fileSink) write(e element) {
	if _, level := e.areaLevel(); level < s.level {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.w == nil {
		return
	}

	if s.now().Format(dayLayout) != s.day {
		s.rotate()
		if s.w == nil {
			return
		}
	}

	s.line(string(e))
}

// rotate closes the file of the day before and opens the next, the caller holds s.mu
func (s *fileSink) rotate() {
	if err := s.w.Flush(); err != nil {
		s.fail(err)
		return
	}

	if err := s.f.Close(); err != nil {
		s.fail(err)
		return
	}

	if err := s.open(); err != nil {
		s.fail(err)
		return
	}

	s.maintainAsync()
}

// failure is what switched the file off
func (s *fileSink) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.err
}

// start flushes every flushEvery
func (s *fileSink) start() {
	s.ticker.Add(1)
	go func() {
		defer s.ticker.Done()

		tick := time.NewTicker(s.flushEvery)
		defer tick.Stop()

		for {
			select {
			case <-tick.C:
				s.flush()
			case <-s.stopC:
				return
			}
		}
	}()
}

func (s *fileSink) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.w != nil {
		if err := s.w.Flush(); err != nil {
			s.fail(err)
		}
	}
}

// close writes out what is buffered and closes the file
func (s *fileSink) close() {
	select {
	case <-s.stopC:
	default:
		close(s.stopC)
	}
	s.ticker.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.w != nil {
		if err := s.w.Flush(); err != nil {
			s.fail(err)
		}
	}

	if s.f != nil {
		_ = s.f.Close()
	}
	s.f, s.w = nil, nil
}

func (s *fileSink) maintainAsync() {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		s.maintain()
	}()
}

// logFile is a file of the folder
type logFile struct {
	name string
	day  time.Time
	size int64
	gz   bool
}

// listFiles returns the files this writes, oldest first
func listFiles(dir string) []logFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var res []logFile
	for _, e := range entries {
		m := fileNameRE.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}

		day, err := time.ParseInLocation(dayLayout, m[1], time.Local)
		info, ierr := e.Info()
		if err != nil || ierr != nil {
			continue
		}

		res = append(res, logFile{e.Name(), day, info.Size(), m[2] != ""})
	}

	slices.SortFunc(res, func(a, b logFile) int {
		if c := a.day.Compare(b.day); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})

	return res
}

// maintain compresses the files of the days before, deletes those older than
// the retention and then the oldest ones while the folder is over its limit.
// Today's file stays.
func (s *fileSink) maintain() {
	maintainMu.Lock()
	defer maintainMu.Unlock()

	now := s.now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)

	// left by a run that was stopped while compressing
	if tmp, err := filepath.Glob(filepath.Join(s.dir, "evcc-*.log.gz.tmp")); err == nil {
		for _, f := range tmp {
			_ = os.Remove(f)
		}
	}

	for _, f := range listFiles(s.dir) {
		if f.gz || !f.day.Before(today) {
			continue
		}
		if err := compress(filepath.Join(s.dir, f.name)); err != nil {
			s.report(jww.LevelWarn, fmt.Sprintf("log file %s not compressed: %v", f.name, err))
		}
	}

	var total int64
	files := listFiles(s.dir)
	for _, f := range files {
		if f.day.Before(today.AddDate(0, 0, -s.days)) {
			if err := os.Remove(filepath.Join(s.dir, f.name)); err != nil {
				s.report(jww.LevelWarn, fmt.Sprintf("log file %s not deleted: %v", f.name, err))
			}
			continue
		}
		total += f.size
	}

	for _, f := range listFiles(s.dir) {
		if total <= s.sizeLimit || !f.day.Before(today) {
			break
		}

		if err := os.Remove(filepath.Join(s.dir, f.name)); err != nil {
			s.report(jww.LevelWarn, fmt.Sprintf("log file %s not deleted: %v", f.name, err))
			continue
		}

		total -= f.size
		s.report(jww.LevelWarn, fmt.Sprintf("log files over %d MB, deleted %s", s.sizeLimit>>20, f.name))
	}
}

func (s *fileSink) report(level jww.Threshold, msg string) {
	if s.log != nil {
		s.log(level, msg)
	}
}

// compress packs a file to path.gz and deletes the original, streaming
func compress(path string) (err error) {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := path + ".gz.tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	zw := gzip.NewWriter(out)
	if _, err = io.Copy(zw, in); err == nil {
		err = zw.Close()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}

	if err = os.Rename(tmp, path+".gz"); err != nil {
		return err
	}

	_ = in.Close()
	return os.Remove(path)
}
