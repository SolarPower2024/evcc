package core

// Custom extension: the log in daily files, set in the ui on the log page. The
// writing is in util/logstash/file_custom.go, this holds the setting, the folder
// and the api. Without a stored setting nothing is written and no folder created.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/logstash"
	jww "github.com/spf13/jwalterweatherman"
)

// logFileMu serializes saving and applying the setting
var logFileMu sync.Mutex

// logFileDir returns the folder of the log files, replaced in tests
var logFileDir = func() string {
	return logFileDirFor("/config", db.FilePath())
}

// logFileDirFor returns /config/logs where /config is a folder (the add-on, reachable through
// its share), else logs next to the database file. Empty without either, e.g. an in-memory database.
func logFileDirFor(configDir, dbFile string) string {
	if fi, err := os.Stat(configDir); err == nil && fi.IsDir() {
		return filepath.Join(configDir, "logs")
	}

	if dbFile == "" || strings.Contains(dbFile, ":memory:") || strings.Contains(dbFile, "mode=memory") {
		return ""
	}

	return filepath.Join(filepath.Dir(dbFile), "logs")
}

// restoreLogFile applies the persisted setting, switched on again after a restart
func (site *Site) restoreLogFile() {
	cfg := logstash.DefaultFileConfig

	if err := settings.Json(keys.LogFile, &cfg); err != nil || cfg.Validate() != nil {
		cfg = logstash.DefaultFileConfig
	}

	site.applyLogFile(cfg)
}

// applyLogFile hands the setting to the writer
func (site *Site) applyLogFile(cfg logstash.FileConfig) {
	log := util.NewLogger("logfil")

	cfg.Level = strings.ToLower(cfg.Level)
	cfg.Dir = logFileDir()
	cfg.Version = util.FormattedVersion()
	cfg.Log = func(level jww.Threshold, msg string) {
		if level >= jww.LevelError {
			log.ERROR.Println(msg)
		} else {
			log.WARN.Println(msg)
		}
	}

	_, _ = logstash.SetFile(cfg)
}

// LogFile returns the log file setting with folder, files and size
func (site *Site) LogFile() logstash.FileState {
	return logstash.FileStatus()
}

// SetLogFile saves the log file setting and applies it
func (site *Site) SetLogFile(cfg logstash.FileConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	cfg.Level = strings.ToLower(cfg.Level)

	logFileMu.Lock()
	defer logFileMu.Unlock()

	if err := settings.SetJson(keys.LogFile, cfg); err != nil {
		return err
	}

	site.applyLogFile(cfg)

	return nil
}
