package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/logstash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useLogFileDir points the log file at a folder of the test and switches it off afterwards
func useLogFileDir(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "logs")

	prev := logFileDir
	logFileDir = func() string { return dir }
	t.Cleanup(func() {
		logFileDir = prev
		_, _ = logstash.SetFile(logstash.DefaultFileConfig)
	})

	return dir
}

func TestLogFileDirFor(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o644))

	// the add-on: /config is a folder and takes precedence over the database
	assert.Equal(t, filepath.Join(root, "logs"), logFileDirFor(root, "/var/lib/evcc/evcc.db"))

	// /config missing or no folder: next to the database
	assert.Equal(t, filepath.Join("/var/lib/evcc", "logs"), logFileDirFor(filepath.Join(root, "none"), "/var/lib/evcc/evcc.db"))
	assert.Equal(t, filepath.Join("/var/lib/evcc", "logs"), logFileDirFor(file, "/var/lib/evcc/evcc.db"))

	// no place to write to
	assert.Empty(t, logFileDirFor(root+"/none", ""))
	assert.Empty(t, logFileDirFor(root+"/none", ":memory:"))
	assert.Empty(t, logFileDirFor(root+"/none", "file::memory:?cache=shared"))
}

// TestLogFileInertWhenUnused: without a stored setting nothing is written and
// no folder created
func TestLogFileInertWhenUnused(t *testing.T) {
	noSettingsDB(t)
	keepSettings(t)
	dir := useLogFileDir(t)

	site := &Site{}
	site.restoreLogFile()

	st := site.LogFile()
	assert.False(t, st.Enabled)
	assert.Equal(t, "debug", st.Level)
	assert.Equal(t, 14, st.Days)
	assert.Equal(t, dir, st.Dir)
	assert.Zero(t, st.Files)
	assert.NoDirExists(t, dir)
}

// TestLogFileSettingSurvivesRestart: set through the api, switched on again by
// restoreLogFile, folder and version are not stored
func TestLogFileSettingSurvivesRestart(t *testing.T) {
	noSettingsDB(t)
	keepSettings(t)
	dir := useLogFileDir(t)

	a := &Site{}
	require.NoError(t, a.SetLogFile(logstash.FileConfig{Enabled: true, Level: "INFO", Days: 30}))

	st := a.LogFile()
	assert.True(t, st.Enabled)
	assert.Empty(t, st.Error)
	assert.Equal(t, "info", st.Level)
	assert.Equal(t, 30, st.Days)
	assert.Equal(t, dir, st.Dir)
	assert.DirExists(t, dir)

	stored, err := settings.String(keys.LogFile)
	require.NoError(t, err)
	assert.JSONEq(t, `{"enabled":true,"level":"info","days":30}`, stored)

	// restart: the writer starts from nothing
	_, err = logstash.SetFile(logstash.DefaultFileConfig)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(dir))

	b := &Site{}
	b.restoreLogFile()

	st = b.LogFile()
	assert.True(t, st.Enabled)
	assert.Equal(t, "info", st.Level)
	assert.Equal(t, 30, st.Days)
	assert.Empty(t, st.Error)
	assert.DirExists(t, dir, "switched on again")

	// switched off: stays off after a restart, files stay
	require.NoError(t, b.SetLogFile(logstash.FileConfig{Level: "info", Days: 30}))
	assert.False(t, b.LogFile().Enabled)
	assert.Equal(t, 1, b.LogFile().Files)

	_, err = logstash.SetFile(logstash.DefaultFileConfig)
	require.NoError(t, err)
	c := &Site{}
	c.restoreLogFile()
	assert.False(t, c.LogFile().Enabled)
}

func TestLogFileInvalidSetting(t *testing.T) {
	noSettingsDB(t)
	keepSettings(t)
	dir := useLogFileDir(t)

	site := &Site{}

	assert.Error(t, site.SetLogFile(logstash.FileConfig{Enabled: true, Level: "verbose", Days: 14}))
	assert.Error(t, site.SetLogFile(logstash.FileConfig{Enabled: true, Level: "debug", Days: 0}))
	assert.Error(t, site.SetLogFile(logstash.FileConfig{Enabled: true, Level: "debug", Days: 91}))

	assert.False(t, settings.Exists(keys.LogFile), "nothing stored")
	assert.False(t, site.LogFile().Enabled)
	assert.NoDirExists(t, dir)
}

// TestLogFileStoredInvalidIsIgnored: a stored setting that no longer validates falls back to off
func TestLogFileStoredInvalidIsIgnored(t *testing.T) {
	noSettingsDB(t)
	keepSettings(t)
	dir := useLogFileDir(t)

	settings.SetString(keys.LogFile, `{"enabled":true,"level":"loud","days":14}`)

	site := &Site{}
	site.restoreLogFile()

	assert.False(t, site.LogFile().Enabled)
	assert.NoDirExists(t, dir)
}
