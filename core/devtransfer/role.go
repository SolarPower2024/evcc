// Package devtransfer is the test version's exchange of data with evcc custom:
// "take over" replaces its database by a copy of the real one, "give back"
// merges what it recorded since into the real file. It is active only with the
// environment variable of the test version's add-on, see README of core/lm.
package devtransfer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/util"
)

// EnvVar holds the glob of the real database, set by the add-on of the test version
const EnvVar = "EVCC_DEVTRANSFER"

var (
	// ErrDisabled is returned without the environment variable
	ErrDisabled = errors.New("test version transfer disabled")
	// ErrNotFound is returned if the glob does not hit exactly one file
	ErrNotFound = errors.New("database of evcc custom not found")
	// ErrNotTaken is returned for giving back before the data was taken over
	ErrNotTaken = errors.New("data of evcc custom not taken over yet")
	// ErrRestored is returned if the take over failed after the data was replaced: the restart is due all the same
	ErrRestored = errors.New("data taken over, but the database did not close cleanly")
	// ErrLocked is returned if the real database is in use
	ErrLocked = errors.New("database of evcc custom is locked")
)

var (
	logMu   sync.Mutex
	lastMsg string
)

// Enabled reports whether this instance is the test version
func Enabled() bool {
	return os.Getenv(EnvVar) != ""
}

// LivePath returns the database file of evcc custom: the glob must hit exactly
// one file that is not this instance's own database
func LivePath() (string, error) {
	pattern := os.Getenv(EnvVar)
	if pattern == "" {
		return "", ErrDisabled
	}

	path, err := livePath(pattern)
	if err != nil {
		logOnce(err)
		return "", err
	}

	return path, nil
}

func livePath(pattern string) (string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotFound, err)
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%w: no file matches %s", ErrNotFound, pattern)
	case 1:
	default:
		return "", fmt.Errorf("%w: %d files match %s", ErrNotFound, len(matches), pattern)
	}

	if own := db.FilePath(); own != "" {
		if same, err := sameFile(matches[0], own); err == nil && same {
			return "", fmt.Errorf("%w: %s is this instance's own database", ErrNotFound, matches[0])
		}
	}

	return matches[0], nil
}

func sameFile(a, b string) (bool, error) {
	ia, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ia, ib), nil
}

// logOnce logs a reason once until it changes
func logOnce(err error) {
	logMu.Lock()
	defer logMu.Unlock()

	if msg := err.Error(); msg != lastMsg {
		lastMsg = msg
		util.NewLogger("devtransfer").WARN.Printf("transfer off: %v", err)
	}
}

// sinceFile is the file next to the own database that marks the take over: its
// content is the start of the test time in Unix nanoseconds
func sinceFile() string {
	return db.FilePath() + ".since"
}

// Since returns the start of the test time, false if nothing was taken over yet
func Since() (time.Time, bool) {
	b, err := os.ReadFile(sinceFile())
	if err != nil {
		return time.Time{}, false
	}

	var ns int64
	if _, err := fmt.Sscanf(string(b), "%d", &ns); err != nil {
		return time.Time{}, false
	}

	return time.Unix(0, ns), true
}

func writeSince(t time.Time) error {
	return os.WriteFile(sinceFile(), fmt.Appendf(nil, "%d", t.UnixNano()), 0o600)
}

// Changed returns when the real database was changed last, including its log
func Changed(path string) time.Time {
	var latest time.Time
	for _, p := range []string{path, path + "-wal"} {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(latest) {
			latest = fi.ModTime()
		}
	}
	return latest
}
