package logstash

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *fileTest) put(t *testing.T, name, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(f.dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644))
}

// TestFileList: newest first, gz recognized, files of other names ignored
func TestFileList(t *testing.T) {
	f := newFileTest(t)

	f.put(t, "evcc-2026-10-03.log.gz", "abc")
	f.put(t, "evcc-2026-10-04.log.gz", "abcdef")
	f.put(t, "notes.txt", "x")
	f.put(t, "evcc-2026-10-02.log.bak", "x")
	require.NoError(t, os.Mkdir(filepath.Join(f.dir, "evcc-2026-10-01.log"), 0o755))

	st, err := f.setFile(f.config("info", 30))
	require.NoError(t, err)

	require.Len(t, st.List, 3)
	assert.Equal(t, FileEntry{"evcc-2026-10-05.log", "2026-10-05", int64(len(header + "info\n")), false, true}, st.List[0])
	assert.Equal(t, FileEntry{"evcc-2026-10-04.log.gz", "2026-10-04", 6, true, false}, st.List[1])
	assert.Equal(t, FileEntry{"evcc-2026-10-03.log.gz", "2026-10-03", 3, true, false}, st.List[2])
	assert.Equal(t, 3, st.Files)
}

// TestFileListDisabled: without the file switched on the folder is still listed,
// without a folder the list is empty and not null
func TestFileListDisabled(t *testing.T) {
	f := newFileTest(t)
	f.put(t, "evcc-2026-10-04.log.gz", "abc")

	cfg := f.config("info", 30)
	cfg.Enabled = false
	st, err := f.setFile(cfg)
	require.NoError(t, err)
	require.Len(t, st.List, 1)
	assert.Equal(t, "evcc-2026-10-04.log.gz", st.List[0].Name)

	// the file is off: none is written, also not one of today
	f.put(t, "evcc-2026-10-05.log", "x")
	for _, e := range f.status().List {
		assert.False(t, e.Current, e.Name)
	}

	cfg.Dir = ""
	st, err = f.setFile(cfg)
	require.NoError(t, err)
	assert.NotNil(t, st.List)
	assert.Empty(t, st.List)
}

func openAll(t *testing.T, f *fileTest, name string) (string, int64) {
	t.Helper()
	rc, size, err := f.openFile(name)
	require.NoError(t, err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(b), size
}

// TestFileOpen: today's file holds the buffered lines, a day before is delivered as it lies
func TestFileOpen(t *testing.T) {
	f := newFileTest(t)
	gz := "\x1f\x8b\x08raw bytes \x00\xff"
	f.put(t, "evcc-2026-10-04.log.gz", gz)

	_, err := f.setFile(f.config("info", 30))
	require.NoError(t, err)

	// below the 2 s ticker and the 64 KB buffer: only OpenFile's flush brings it to disk
	f.write(lInfo)
	b, err := os.ReadFile(filepath.Join(f.dir, "evcc-2026-10-05.log"))
	require.NoError(t, err)
	assert.NotContains(t, string(b), lInfo)

	got, size := openAll(t, f, "evcc-2026-10-05.log")
	assert.Equal(t, header+"info\n"+lInfo+"\n", got)
	assert.EqualValues(t, len(got), size)

	got, size = openAll(t, f, "evcc-2026-10-04.log.gz")
	assert.Equal(t, gz, got)
	assert.EqualValues(t, len(gz), size)
}

// TestFileOpenLimited: a file that grows after opening ends at the size it had
func TestFileOpenLimited(t *testing.T) {
	f := newFileTest(t)
	_, err := f.setFile(f.config("info", 30))
	require.NoError(t, err)

	rc, size, err := f.openFile("evcc-2026-10-05.log")
	require.NoError(t, err)
	defer rc.Close()

	f.write(lInfo, lWarn) // warn is flushed at once
	f.l.mu.RLock()
	f.sink.flush()
	f.l.mu.RUnlock()

	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.EqualValues(t, size, len(b))
	assert.Equal(t, header+"info\n", string(b))
}

// TestFileOpenRejects: only the names of the pattern, only regular files of the folder
func TestFileOpenRejects(t *testing.T) {
	f := newFileTest(t)
	f.put(t, "evcc-2026-10-04.log.gz", "abc")
	f.put(t, "other.log", "x")

	outside := filepath.Join(t.TempDir(), "secret.log")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(f.dir, "evcc-2026-10-02.log"), 0o755))

	_, err := f.setFile(f.config("info", 30))
	require.NoError(t, err)
	f.waitMaintain() // the clean-up would compress the link
	if err := os.Symlink(outside, filepath.Join(f.dir, "evcc-2026-10-03.log")); err != nil {
		t.Log("no symlinks here:", err)
	}

	for _, name := range []string{
		"", "../x", "../evcc-2026-10-04.log.gz", "a/evcc-2026-10-04.log.gz", `a\evcc-2026-10-04.log.gz`,
		"/evcc-2026-10-04.log.gz", "other.log", "evcc-2026-10-04.log.gz.tmp", "evcc-2026-10-04.log\n",
		"evcc-2026-10-03.log", // symlink
		"evcc-2026-10-02.log", // folder
		"evcc-2026-10-01.log", // missing
	} {
		_, _, err := f.openFile(name)
		assert.ErrorIs(t, err, fs.ErrNotExist, "%q", name)
	}

	f.waitMaintain()
	// without a folder
	_, err = f.setFile(FileConfig{Level: "info", Days: 7})
	require.NoError(t, err)
	_, _, err = f.openFile("evcc-2026-10-04.log.gz")
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

// TestFileListCurrent: only the file being written is current, after switching off none
func TestFileListCurrent(t *testing.T) {
	f := newFileTest(t)
	f.put(t, "evcc-2026-10-04.log.gz", "abc")
	f.put(t, "evcc-2026-10-05.log", "from before\n") // today's file of an earlier run

	st, err := f.setFile(f.config("info", 30))
	require.NoError(t, err)
	require.Len(t, st.List, 2)
	assert.True(t, st.List[0].Current)
	assert.Equal(t, "evcc-2026-10-05.log", st.List[0].Name)
	assert.False(t, st.List[1].Current)

	// a new day: the first line switches, the file of the day before is no longer current
	f.setTime(time.Date(2026, 10, 6, 0, 0, 1, 0, time.Local))
	f.write(lInfo)
	f.waitMaintain()
	st = f.status()
	require.NotEmpty(t, st.List)
	assert.Equal(t, "evcc-2026-10-06.log", st.List[0].Name)
	for i, e := range st.List {
		assert.Equal(t, i == 0, e.Current, e.Name)
	}

	cfg := f.config("info", 30)
	cfg.Enabled = false
	st, err = f.setFile(cfg)
	require.NoError(t, err)
	for _, e := range st.List {
		assert.False(t, e.Current, e.Name)
	}
}
