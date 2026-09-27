package circuit

// Custom extension: load management sheds loads by priority when a circuit's
// power limit is exceeded, so over power is expected and handled, not a fault.
// It is logged at INFO, which keeps it in the log without popping up as a ui
// notification (only WARN and ERROR are captured for the ui, see util/log.go).
// Over current (fuses) stays a warning.

import "log"

// overPowerLog is the logger for an exceeded power limit
func (c *Circuit) overPowerLog() *log.Logger {
	return c.log.INFO
}
