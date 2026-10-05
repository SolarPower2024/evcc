package logstash

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	jww "github.com/spf13/jwalterweatherman"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fileTest is a log buffer with a file output, a clock and a folder of its own
type fileTest struct {
	*tee
	dir string

	mu   sync.Mutex
	time time.Time
	logs []string
}

func newFileTest(t *testing.T) *fileTest {
	t.Helper()

	f := &fileTest{
		tee:  newTee(New(10000)),
		dir:  filepath.Join(t.TempDir(), "logs"), // does not exist yet
		time: time.Date(2026, 10, 5, 12, 0, 0, 0, time.Local),
	}
	f.tee.now = f.clock
	f.tee.flushEvery = time.Hour // flushed by close, unless a test wants the ticker

	t.Cleanup(f.tee.close)

	return f
}

func (f *fileTest) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.time
}

func (f *fileTest) setTime(tm time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.time = tm
}

func (f *fileTest) config(level string, days int) FileConfig {
	return FileConfig{
		Enabled: true,
		Level:   level,
		Days:    days,
		Dir:     f.dir,
		Version: "0.1.2",
		Log: func(l jww.Threshold, msg string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.logs = append(f.logs, fmt.Sprintf("%d %s", l, msg))
		},
	}
}

func (f *fileTest) logged() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.logs)
}

// waitMaintain waits for the clean-up that switching on or a new day started
func (f *fileTest) waitMaintain() {
	f.l.mu.RLock()
	s := f.sink
	f.l.mu.RUnlock()

	if s != nil {
		s.bg.Wait()
	}
}

func (f *fileTest) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	require.NoError(t, err)
	return string(b)
}

func (f *fileTest) names(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(f.dir)
	require.NoError(t, err)

	var res []string
	for _, e := range entries {
		res = append(res, e.Name())
	}
	return res
}

func readGz(t *testing.T, path string) string {
	t.Helper()
	in, err := os.Open(path)
	require.NoError(t, err)
	defer in.Close()

	zr, err := gzip.NewReader(in)
	require.NoError(t, err)
	b, err := io.ReadAll(zr)
	require.NoError(t, err)
	return string(b)
}

func (f *fileTest) write(lines ...string) {
	for _, s := range lines {
		_, _ = f.Write([]byte(s))
	}
}

const header = "[logfil] INFO 2026/10/05 12:00:00 evcc 0.1.2, level "

var (
	lTrace = "[mqtt  ] TRACE 2026/10/05 12:00:01 trace"
	lDebug = "[site  ] DEBUG 2026/10/05 12:00:02 debug"
	lInfo  = "[site  ] INFO 2026/10/05 12:00:03 info"
	lWarn  = "[site  ] WARN 2026/10/05 12:00:04 warn"
	lError = "[site  ] ERROR 2026/10/05 12:00:05 error"
)

// TestFileInert: without SetFile nothing is written and no folder created, the
// log buffer gets every line as without the output
func TestFileInert(t *testing.T) {
	f := newFileTest(t)
	ref := New(10000)

	for _, s := range []string{lTrace, lDebug, lInfo, "[cache ] DEBUG dropped", lWarn, lError, "no header"} {
		f.write(s)
		_, _ = ref.Write([]byte(s))
	}

	assert.Equal(t, ref.All(nil, jww.LevelTrace, 0), f.l.All(nil, jww.LevelTrace, 0), "same buffer as without the output")
	assert.Len(t, f.l.All(nil, jww.LevelTrace, 0), 6)

	assert.NoDirExists(t, f.dir)
	st := f.status()
	assert.False(t, st.Enabled)
	assert.Equal(t, "debug", st.Level)
	assert.Equal(t, 14, st.Days)
	assert.Empty(t, st.Dir)
	assert.Zero(t, st.Files)

	f.close() // nothing to close
	assert.NoDirExists(t, f.dir)
}

// TestFileOutputIsBuffer: the package output reaches the buffer of the log
// page unchanged, the contract util.newLogger relies on
func TestFileOutputIsBuffer(t *testing.T) {
	line := "[tstout] DEBUG 2026/10/05 12:00:00 reaches the log page"

	n, err := Output.Write([]byte(line))
	require.NoError(t, err)
	assert.Equal(t, len(line), n)

	assert.Contains(t, All([]string{"tstout"}, jww.LevelTrace, 0), line)

	_, _ = Output.Write([]byte("[cache ] DEBUG dropped as in the buffer"))
	assert.Empty(t, All([]string{"cache"}, jww.LevelTrace, 0))
}

func TestFileLevelFilter(t *testing.T) {
	for level, want := range map[string][]string{
		"error": {lError},
		"warn":  {lWarn, lError},
		"info":  {lInfo, lWarn, lError},
		"debug": {lDebug, lInfo, lWarn, lError},
		"trace": {lTrace, lDebug, lInfo, lWarn, lError},
	} {
		t.Run(level, func(t *testing.T) {
			f := newFileTest(t)

			st, err := f.setFile(f.config(level, 14))
			require.NoError(t, err)
			assert.True(t, st.Enabled)
			assert.Empty(t, st.Error)

			f.write(lTrace, lDebug, lInfo, lWarn, lError)
			f.close()

			exp := header + level + "\n"
			for _, s := range want {
				exp += s + "\n"
			}
			assert.Equal(t, exp, f.read(t, "evcc-2026-10-05.log"))
		})
	}
}

// TestFileDumpsBuffer: switching on writes the buffer in the order the lines
// came, trace and other lines mixed, filtered by level, without cache lines
func TestFileDumpsBuffer(t *testing.T) {
	f := newFileTest(t)

	f.write(lTrace, lDebug, "[cache ] DEBUG dropped", lInfo, "[mqtt  ] TRACE 2026/10/05 12:00:02 trace 2", lError)

	_, err := f.setFile(f.config("debug", 14))
	require.NoError(t, err)

	f.write(lWarn)
	f.close()

	assert.Equal(t, header+"debug\n"+lDebug+"\n"+lInfo+"\n"+lError+"\n"+lWarn+"\n", f.read(t, "evcc-2026-10-05.log"))

	// switched on again: appended, with a header of its own, the lines already in the file not again
	_, err = f.setFile(f.config("error", 14))
	require.NoError(t, err)
	f.close()

	got := f.read(t, "evcc-2026-10-05.log")
	assert.Equal(t, 2, strings.Count(got, "[logfil] INFO"))
	assert.Equal(t, 1, strings.Count(got, lError))
	assert.True(t, strings.HasSuffix(got, lWarn+"\n"+header+"error\n"))
}

// TestFileSettingChangeNoDuplicate: changing a setting while the file is on
// reopens it without writing the buffer a second time
func TestFileSettingChangeNoDuplicate(t *testing.T) {
	f := newFileTest(t)

	_, err := f.setFile(f.config("debug", 14))
	require.NoError(t, err)
	f.write(lDebug, lInfo)

	_, err = f.setFile(f.config("info", 30))
	require.NoError(t, err)
	f.write(lWarn)
	f.close()

	assert.Equal(t, header+"debug\n"+lDebug+"\n"+lInfo+"\n"+header+"info\n"+lWarn+"\n", f.read(t, "evcc-2026-10-05.log"))
}

// TestFileReenableWritesMissed: lines that came while the file was off are
// written on switching on again, those already in the file are not
func TestFileReenableWritesMissed(t *testing.T) {
	f := newFileTest(t)

	_, err := f.setFile(f.config("debug", 14))
	require.NoError(t, err)
	f.write(lDebug)

	off := f.config("debug", 14)
	off.Enabled = false
	_, err = f.setFile(off)
	require.NoError(t, err)
	f.write(lInfo)

	_, err = f.setFile(f.config("debug", 14))
	require.NoError(t, err)
	f.close()

	assert.Equal(t, header+"debug\n"+lDebug+"\n"+header+"debug\n"+lInfo+"\n", f.read(t, "evcc-2026-10-05.log"))
}

// TestFileFlushesWarnAtOnce: warnings and errors are on disk right away, the
// lines before a crash are not lost in the buffer
func TestFileFlushesWarnAtOnce(t *testing.T) {
	f := newFileTest(t)

	_, err := f.setFile(f.config("debug", 14))
	require.NoError(t, err)

	f.write(lInfo)
	assert.NotContains(t, f.read(t, "evcc-2026-10-05.log"), lInfo, "info waits for the flush interval")

	f.write(lWarn)
	got := f.read(t, "evcc-2026-10-05.log")
	assert.Contains(t, got, lInfo+"\n"+lWarn+"\n")

	f.write(lError)
	assert.Contains(t, f.read(t, "evcc-2026-10-05.log"), lError)
}

// TestWriteWithIsWrite: writeWith stores exactly what evcc's Write stores, so
// a change of Write in an evcc update shows up here
func TestWriteWithIsWrite(t *testing.T) {
	lines := []string{lTrace, lDebug, "[cache ] DEBUG dropped", lInfo, "no area at all", lWarn, lError}

	a, b := New(5), New(5)
	for _, s := range lines {
		na, ea := a.Write([]byte(s))
		nb, eb := b.writeWith([]byte(s), func(entry) {})
		assert.Equal(t, na, nb, s)
		assert.Equal(t, ea, eb, s)
	}

	for _, level := range []jww.Threshold{jww.LevelTrace, jww.LevelDebug, jww.LevelError} {
		assert.Equal(t, a.All(nil, level, 0), b.All(nil, level, 0))
	}
	assert.Equal(t, a.Areas(), b.Areas())
	assert.Equal(t, a.Size(), b.Size())
	assert.Equal(t, a.seq, b.seq)
}

// TestFileDumpNoGapNoDuplicate: lines written by other goroutines while the
// file is switched on end up in the file exactly once, in order
func TestFileDumpNoGapNoDuplicate(t *testing.T) {
	f := newFileTest(t)

	const writers, lines = 8, 1000

	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := range writers {
		wg.Go(func() {
			<-start
			for i := range lines {
				f.write(fmt.Sprintf("[site  ] INFO 2026/10/05 12:00:00 w%d-%04d", w, i))
				if i%100 == 0 {
					time.Sleep(time.Millisecond)
				}
			}
		})
	}

	close(start)
	time.Sleep(2 * time.Millisecond)
	_, err := f.setFile(f.config("info", 14))
	require.NoError(t, err)

	wg.Wait()
	f.close()

	seen := make(map[string]int)
	next := make(map[int]int)
	for _, l := range strings.Split(strings.TrimSpace(f.read(t, "evcc-2026-10-05.log")), "\n")[1:] {
		id := l[strings.LastIndex(l, " ")+1:]
		seen[id]++

		var w, i int
		_, err := fmt.Sscanf(id, "w%d-%d", &w, &i)
		require.NoError(t, err)
		assert.Equal(t, next[w], i, "writer %d in order", w)
		next[w] = i + 1
	}

	assert.Len(t, seen, writers*lines)
	for id, n := range seen {
		assert.Equal(t, 1, n, id)
	}
}

func TestFileDayRotation(t *testing.T) {
	f := newFileTest(t)

	_, err := f.setFile(f.config("debug", 14))
	require.NoError(t, err)
	f.write(lInfo)

	// the first line of the next day switches the file
	f.setTime(time.Date(2026, 10, 6, 0, 0, 3, 0, time.Local))
	f.write(lError)
	f.waitMaintain()
	f.close()

	assert.ElementsMatch(t, []string{"evcc-2026-10-05.log.gz", "evcc-2026-10-06.log"}, f.names(t), "the day before is packed, the original gone")
	assert.Equal(t, header+"debug\n"+lInfo+"\n", readGz(t, filepath.Join(f.dir, "evcc-2026-10-05.log.gz")))

	day2 := f.read(t, "evcc-2026-10-06.log")
	assert.True(t, strings.HasPrefix(day2, "[logfil] INFO 2026/10/06 00:00:03 evcc 0.1.2, level debug\n"), day2)
	assert.True(t, strings.HasSuffix(day2, lError+"\n"))
}

// TestFileRetention: older than the retention is deleted when switching on and
// at a new day; files the day before that were left unpacked get packed
func TestFileRetention(t *testing.T) {
	f := newFileTest(t)
	require.NoError(t, os.MkdirAll(f.dir, 0o755))

	touch := func(name, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644))
	}
	touch("evcc-2026-09-20.log.gz", "old")  // 15 days: deleted
	touch("evcc-2026-09-29.log.gz", "old")  // 7th day counting today: kept
	touch("evcc-2026-09-28.log", "old")     // 8th day: deleted
	touch("evcc-2026-10-03.log", "left\n")  // not packed yet
	touch("evcc-2026-10-01.log.gz.tmp", "") // stopped while packing
	touch("notes.txt", "not ours")
	touch("evcc-x.log", "not ours")

	_, err := f.setFile(f.config("debug", 7))
	require.NoError(t, err)
	f.waitMaintain()

	assert.ElementsMatch(t, []string{
		"evcc-2026-09-29.log.gz", "evcc-2026-10-03.log.gz", "evcc-2026-10-05.log", "notes.txt", "evcc-x.log",
	}, f.names(t))
	assert.Equal(t, "left\n", readGz(t, filepath.Join(f.dir, "evcc-2026-10-03.log.gz")))

	// a new day moves the window
	f.setTime(time.Date(2026, 10, 6, 0, 0, 1, 0, time.Local))
	f.write(lInfo)
	f.waitMaintain()

	assert.NotContains(t, f.names(t), "evcc-2026-09-29.log.gz")
	assert.Contains(t, f.names(t), "evcc-2026-10-03.log.gz")
	assert.Contains(t, f.names(t), "evcc-2026-10-05.log.gz")
}

// TestFileSizeLimit: over the limit the oldest files go with a warning
func TestFileSizeLimit(t *testing.T) {
	f := newFileTest(t)
	f.sizeLimit = 150
	require.NoError(t, os.MkdirAll(f.dir, 0o755))

	for _, d := range []string{"01", "02", "03"} {
		require.NoError(t, os.WriteFile(filepath.Join(f.dir, "evcc-2026-10-"+d+".log.gz"), make([]byte, 60), 0o644))
	}

	_, err := f.setFile(f.config("debug", 14))
	require.NoError(t, err)
	f.waitMaintain()

	// 180 and the header (58 bytes) over 150: the two oldest go
	assert.ElementsMatch(t, []string{"evcc-2026-10-03.log.gz", "evcc-2026-10-05.log"}, f.names(t))

	warns := f.logged()
	require.Len(t, warns, 2)
	for i, d := range []string{"01", "02"} {
		assert.Contains(t, warns[i], fmt.Sprintf("%d ", jww.LevelWarn))
		assert.Contains(t, warns[i], "evcc-2026-10-"+d+".log.gz")
	}
}

// TestFileSizeLimitKeepsToday: today's file is never deleted; once it alone
// reaches the limit, writing pauses with a note until the next day
func TestFileSizeLimitKeepsToday(t *testing.T) {
	f := newFileTest(t)
	f.sizeLimit = 100

	_, err := f.setFile(f.config("debug", 14))
	require.NoError(t, err)
	f.waitMaintain()

	big := "[site  ] INFO " + strings.Repeat("x", 300)
	f.write(big, lError)
	f.l.mu.RLock()
	f.sink.flush()
	f.l.mu.RUnlock()
	f.sink.maintain()

	assert.Equal(t, []string{"evcc-2026-10-05.log"}, f.names(t))
	got := f.read(t, "evcc-2026-10-05.log")
	assert.Contains(t, got, big+"\n[logfil] WARN 2026/10/05 12:00:00 log file reached 0 MB, paused until the next day\n")
	assert.NotContains(t, got, lError, "paused")
	assert.Eventually(t, func() bool { return len(f.logged()) == 1 }, time.Second, time.Millisecond)

	// the next day writes again
	f.setTime(time.Date(2026, 10, 6, 0, 0, 1, 0, time.Local))
	f.write(lInfo)
	f.waitMaintain()
	f.close()
	assert.Contains(t, f.read(t, "evcc-2026-10-06.log"), lInfo)
}

// TestFileSizeLimitDuringDay: today's file growing over the limit removes the
// oldest files right away, not only at the next day
func TestFileSizeLimitDuringDay(t *testing.T) {
	f := newFileTest(t)
	f.sizeLimit = 300
	require.NoError(t, os.MkdirAll(f.dir, 0o755))

	for _, d := range []string{"01", "02", "03"} {
		require.NoError(t, os.WriteFile(filepath.Join(f.dir, "evcc-2026-10-"+d+".log.gz"), make([]byte, 60), 0o644))
	}

	_, err := f.setFile(f.config("debug", 14))
	require.NoError(t, err)
	f.waitMaintain()
	assert.Len(t, f.names(t), 4, "180 and the header within 300")

	// header 58 + 100 + 180 over 300: the oldest goes
	f.write("[site  ] INFO " + strings.Repeat("x", 85))
	f.waitMaintain()

	assert.ElementsMatch(t, []string{"evcc-2026-10-02.log.gz", "evcc-2026-10-03.log.gz", "evcc-2026-10-05.log"}, f.names(t))
}

// TestFileSizeLimitKeepsNewer: only as many of the oldest go as needed
func TestFileSizeLimitKeepsNewer(t *testing.T) {
	f := newFileTest(t)
	f.sizeLimit = 200
	require.NoError(t, os.MkdirAll(f.dir, 0o755))

	for _, d := range []string{"01", "02", "03", "04"} {
		require.NoError(t, os.WriteFile(filepath.Join(f.dir, "evcc-2026-10-"+d+".log.gz"), make([]byte, 60), 0o644))
	}

	_, err := f.setFile(f.config("debug", 14))
	require.NoError(t, err)
	f.waitMaintain()

	// 240 and the header over 200: the two oldest go
	assert.ElementsMatch(t, []string{"evcc-2026-10-03.log.gz", "evcc-2026-10-04.log.gz", "evcc-2026-10-05.log"}, f.names(t))
}

// TestFileErrorFolder: a folder that cannot be created is reported once, shown
// in the state, and the log buffer keeps working
func TestFileErrorFolder(t *testing.T) {
	f := newFileTest(t)

	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o644))

	cfg := f.config("debug", 14)
	cfg.Dir = filepath.Join(blocker, "logs")

	st, err := f.setFile(cfg)
	require.NoError(t, err, "a file that cannot be written is no invalid setting")
	assert.True(t, st.Enabled, "the setting stays on")
	assert.NotEmpty(t, st.Error)

	f.write(lInfo)
	assert.Equal(t, []string{lInfo}, f.l.All(nil, jww.LevelTrace, 0))

	assert.Eventually(t, func() bool { return len(f.logged()) == 1 }, time.Second, time.Millisecond)
	assert.Contains(t, f.logged()[0], fmt.Sprintf("%d ", jww.LevelError))

	assert.NotEmpty(t, f.status().Error)

	// no folder at all
	cfg.Dir = ""
	st, err = f.setFile(cfg)
	require.NoError(t, err)
	assert.NotEmpty(t, st.Error)
}

// TestFileErrorWriting: a file that fails while running switches itself off
// once, the buffer and the other outputs go on
func TestFileErrorWriting(t *testing.T) {
	f := newFileTest(t)

	_, err := f.setFile(f.config("debug", 14))
	require.NoError(t, err)

	// the disk fails: the file is gone from under the writer
	f.l.mu.RLock()
	require.NoError(t, f.sink.f.Close())
	f.l.mu.RUnlock()

	line := "[site  ] INFO 2026/10/05 12:00:00 " + strings.Repeat("x", 1000)
	for range 100 { // more than the 64 KB buffer
		f.write(line)
	}

	assert.Len(t, f.l.All(nil, jww.LevelTrace, 0), 100, "the log page is not affected")

	st := f.status()
	assert.True(t, st.Enabled)
	assert.NotEmpty(t, st.Error)

	f.l.mu.RLock()
	assert.Nil(t, f.sink, "off for this run")
	f.l.mu.RUnlock()

	assert.Eventually(t, func() bool { return len(f.logged()) == 1 }, time.Second, time.Millisecond)
	assert.Contains(t, f.logged()[0], fmt.Sprintf("%d ", jww.LevelError))

	// a new setting tries again
	st, err = f.setFile(f.config("debug", 14))
	require.NoError(t, err)
	assert.Empty(t, st.Error)
}

func TestFileDisable(t *testing.T) {
	f := newFileTest(t)

	_, err := f.setFile(f.config("debug", 14))
	require.NoError(t, err)
	f.write(lInfo)

	st, err := f.setFile(FileConfig{Level: "debug", Days: 14, Dir: f.dir})
	require.NoError(t, err)
	assert.False(t, st.Enabled)
	assert.Equal(t, 1, st.Files, "files stay")
	assert.Positive(t, st.Size)

	f.write(lError)
	assert.Equal(t, header+"debug\n"+lInfo+"\n", f.read(t, "evcc-2026-10-05.log"), "flushed on switching off, nothing after")
}

func TestFileValidate(t *testing.T) {
	f := newFileTest(t)

	for _, cfg := range []FileConfig{
		{Enabled: true, Level: "verbose", Days: 14, Dir: f.dir},
		{Enabled: true, Level: "", Days: 14, Dir: f.dir},
		{Enabled: true, Level: "debug", Days: 0, Dir: f.dir},
		{Enabled: true, Level: "debug", Days: 91, Dir: f.dir},
	} {
		_, err := f.setFile(cfg)
		assert.Error(t, err, "%+v", cfg)
	}
	assert.NoDirExists(t, f.dir)
	assert.False(t, f.status().Enabled)

	for _, l := range []string{"error", "WARN", "Info", "debug", "trace"} {
		assert.NoError(t, FileConfig{Level: l, Days: 90}.Validate(), l)
	}
}

// TestFileFlushes: written out every flush interval, not only on closing
func TestFileFlushes(t *testing.T) {
	f := newFileTest(t)
	f.flushEvery = 5 * time.Millisecond

	_, err := f.setFile(f.config("debug", 14))
	require.NoError(t, err)
	f.write(lInfo)

	assert.Eventually(t, func() bool {
		b, _ := os.ReadFile(filepath.Join(f.dir, "evcc-2026-10-05.log"))
		return strings.Contains(string(b), lInfo)
	}, 2*time.Second, 5*time.Millisecond)
}

// TestFileLineWithoutNewline: every line ends in a newline in the file
func TestFileLineWithoutNewline(t *testing.T) {
	f := newFileTest(t)
	_, err := f.setFile(f.config("debug", 14))
	require.NoError(t, err)

	f.write(lInfo, lError+"\n", lWarn)
	f.close()

	assert.Equal(t, header+"debug\n"+lInfo+"\n"+lError+"\n"+lWarn+"\n", f.read(t, "evcc-2026-10-05.log"))
}

// TestFileStatus: folder, number and size of the files
func TestFileStatus(t *testing.T) {
	f := newFileTest(t)

	st, err := f.setFile(f.config("info", 30))
	require.NoError(t, err)
	assert.Equal(t, f.dir, st.Dir)
	assert.Equal(t, "info", st.Level)
	assert.Equal(t, 30, st.Days)
	assert.Equal(t, 1, st.Files)
	assert.EqualValues(t, len(header+"info\n"), st.Size, "the header is on disk right away")

	f.write(lInfo)
	f.l.mu.RLock()
	f.sink.flush()
	f.l.mu.RUnlock()

	st = f.status()
	assert.Equal(t, 1, st.Files)
	assert.EqualValues(t, len(header+"info\n"+lInfo+"\n"), st.Size)
}
