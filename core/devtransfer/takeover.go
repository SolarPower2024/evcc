package devtransfer

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
)

// supervisorURL is the restart route of the Home Assistant supervisor
var supervisorURL = "http://supervisor/addons/self/restart"

// Exit ends the process like the restore of a database does, replaced in tests
var Exit = func() {
	if p, err := os.FindProcess(os.Getpid()); err == nil {
		_ = p.Signal(os.Interrupt)
	}
}

// Restart ways
const (
	RestartSupervisor = "supervisor"
	RestartExit       = "exit"
)

// RestartWay tells how the test version restarts after the take over: through
// the supervisor if it hands out a token, else it ends and the watchdog or the
// user starts it
func RestartWay() string {
	if os.Getenv("SUPERVISOR_TOKEN") != "" {
		return RestartSupervisor
	}
	return RestartExit
}

// Restart restarts the test version, through the supervisor if possible, else
// it ends the process
func Restart(ctx context.Context) {
	if RestartWay() == RestartSupervisor {
		err := supervisorRestart(ctx)
		if err == nil {
			return
		}
		util.NewLogger("devtransfer").WARN.Printf("supervisor restart: %v", err)
	}

	Exit()
}

func supervisorRestart(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, supervisorURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("SUPERVISOR_TOKEN"))

	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("status %s", resp.Status)
	}
	return nil
}

// TakeoverInfo is what the preview of the take over shows
type TakeoverInfo struct {
	Path    string    `json:"path"`
	Changed time.Time `json:"changed"`
}

// GetTakeoverInfo returns path and last change of the real database
func GetTakeoverInfo() (TakeoverInfo, error) {
	path, err := LivePath()
	if err != nil {
		return TakeoverInfo{}, err
	}

	return TakeoverInfo{path, Changed(path)}, nil
}

// Takeover replaces the database of this instance by a copy of the real one,
// with what this instance recorded since the real one stopped recording. The
// copy goes through sqlite's backup, so it holds what a crash left in the log.
// backup makes the local backup of the own database first. The database is closed
// at the end, the caller flushes its answer and calls Restart.
func Takeover(ctx context.Context, backup func(context.Context) error) error {
	path, err := LivePath()
	if err != nil {
		return err
	}

	if err := settings.Persist(); err != nil {
		return err
	}

	if backup != nil {
		if err := backup(ctx); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}

	tmp, err := os.CreateTemp("", "evcc-takeover-*.db")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	if err := backupFile(ctx, path, tmp.Name()); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	since, err := recorded(ctx, tmp.Name())
	if err != nil {
		return fmt.Errorf("since: %w", err)
	}

	// what this instance recorded since stays
	if _, err := merge(ctx, tmp.Name(), db.FilePath(), since, DefaultItems, nil); err != nil {
		return fmt.Errorf("merge: %w", err)
	}

	if err := db.Restore(ctx, tmp.Name()); err != nil {
		return fmt.Errorf("restore: %w", err)
	}

	if err := writeSince(since); err != nil {
		return err
	}

	// checkpoint the log into the file before the shutdown hooks run
	return db.Close()
}

// recorded returns the end of the recording of the database file
func recorded(ctx context.Context, path string) (time.Time, error) {
	d, err := openFile(path)
	if err != nil {
		return time.Time{}, err
	}
	defer d.Close()

	return latest(ctx, d, "")
}
