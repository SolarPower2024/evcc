package logstash

// Custom extension: list and download of the log files, see file_custom.go.
// The log page's dialog shows the files of the folder and each can be
// downloaded. Only names of the pattern the file writes are handed out, and
// only regular files in the folder.

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// FileEntry is a file of the log folder as the ui lists it
type FileEntry struct {
	Name string `json:"name"`
	Day  string `json:"day"` // YYYY-MM-DD
	Size int64  `json:"size"`
	Gz   bool   `json:"gz"`
	// Current is the file being written now, none while the log file is off
	Current bool `json:"current"`
}

func (f logFile) entry() FileEntry {
	return FileEntry{Name: f.name, Day: f.day.Format(dayLayout), Size: f.size, Gz: f.gz}
}

// readCloser reads at most the size the file had when it was opened
type readCloser struct {
	io.Reader
	io.Closer
}

// OpenFile opens a log file of the folder for download and returns it with its
// size. A file that is still written is limited to that size, so it ends cleanly.
// Today's file is flushed first. Every name that does not belong is fs.ErrNotExist.
func OpenFile(name string) (io.ReadCloser, int64, error) {
	return out.openFile(name)
}

func (t *tee) openFile(name string) (io.ReadCloser, int64, error) {
	if !fileNameRE.MatchString(name) || filepath.Base(name) != name {
		return nil, 0, fs.ErrNotExist
	}

	t.l.mu.RLock()
	dir, sink := t.cfg.Dir, t.sink
	t.l.mu.RUnlock()

	if dir == "" {
		return nil, 0, fs.ErrNotExist
	}

	if sink != nil {
		sink.flushFile(name)
	}

	path := filepath.Join(dir, name)

	// no symlink, nothing but a regular file
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		return nil, 0, fs.ErrNotExist
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}

	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, 0, fs.ErrNotExist
	}

	return readCloser{io.LimitReader(f, fi.Size()), f}, fi.Size(), nil
}

// flushFile writes out the buffer if name is the file being written
func (s *fileSink) flushFile(name string) {
	s.mu.Lock()
	cur := s.f != nil && filepath.Base(s.f.Name()) == name
	s.mu.Unlock()

	if cur {
		s.flush()
	}
}

// currentFile returns the name of the file being written, empty if none
func (s *fileSink) currentFile() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.f == nil || s.w == nil {
		return ""
	}

	return filepath.Base(s.f.Name())
}
