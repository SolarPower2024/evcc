package logstash_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/logstash"
	jww "github.com/spf13/jwalterweatherman"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoggerWritesToLogFile pins the hook in newLogger: every line of every
// level, redacted, reaches the log file as well as the log page. The buffer
// content before switching on is in the file. An external test, as the lines
// land in the global log buffer that the tests of util count. The secret is as
// long as its replacement: evcc drops a line from the log page if redaction
// changes its length (io.MultiWriter stops at the short write of the console).
func TestLoggerWritesToLogFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	log := util.NewLogger("tstfile").Redact("pwd")
	log.DEBUG.Print("before the file")

	// inert until switched on
	assert.NoDirExists(t, dir)

	st, err := logstash.SetFile(logstash.FileConfig{Enabled: true, Level: "trace", Days: 14, Dir: dir, Version: "test"})
	require.NoError(t, err)
	require.Empty(t, st.Error)
	t.Cleanup(func() {
		_, _ = logstash.SetFile(logstash.FileConfig{Level: "trace", Days: 14, Dir: dir})
	})

	log.TRACE.Print("a trace line")
	log.INFO.Print("password pwd")
	log.ERROR.Print("an error")

	// the log page keeps its content
	page := logstash.All([]string{"tstfile"}, jww.LevelTrace, 0)
	assert.Len(t, page, 4)

	logstash.CloseFile()

	files, err := filepath.Glob(filepath.Join(dir, "evcc-*.log"))
	require.NoError(t, err)
	require.Len(t, files, 1)

	b, err := os.ReadFile(files[0])
	require.NoError(t, err)
	got := string(b)

	assert.Contains(t, got, "before the file")
	assert.Contains(t, got, "a trace line")
	assert.Contains(t, got, "an error")
	assert.NotContains(t, got, "pwd", "redacted before the file")
	assert.Contains(t, got, "password")

	// the file holds what the log page holds
	assert.Contains(t, got, strings.Join(page, ""))
}

// TestCloseFileWritesOut: what the shutdown hook calls writes the buffered
// lines out, without waiting for the flush interval
func TestCloseFileWritesOut(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")

	_, err := logstash.SetFile(logstash.FileConfig{Enabled: true, Level: "info", Days: 14, Dir: dir})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = logstash.SetFile(logstash.FileConfig{Level: "info", Days: 14, Dir: dir})
	})

	util.NewLogger("tstclose").INFO.Print("last line before exit")
	logstash.CloseFile()

	files, err := filepath.Glob(filepath.Join(dir, "evcc-*.log"))
	require.NoError(t, err)
	require.Len(t, files, 1)

	b, err := os.ReadFile(files[0])
	require.NoError(t, err)
	assert.Contains(t, string(b), "last line before exit")

	// nothing is written after closing
	util.NewLogger("tstclose").INFO.Print("after")
	time.Sleep(10 * time.Millisecond)
	b, err = os.ReadFile(files[0])
	require.NoError(t, err)
	assert.NotContains(t, string(b), "after")
}
