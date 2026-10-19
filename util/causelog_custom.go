package util

// Custom extension: logs a recurring failure once per cause. A new cause is a
// warning, the same cause again only at debug level, and the first success after
// a failure says "ok again" once. Same rule as logWrite in core/site_peakshaving.go,
// without a quiet time.

import "sync"

// CauseLog remembers the cause of the current failure. The zero value is ready to
// use and safe for concurrent use.
type CauseLog struct {
	mu     sync.Mutex
	cause  string
	failed bool
}

// Failed records a failure and reports whether the cause is new, that is the
// first failure or another cause than the one before
func (c *CauseLog) Failed(cause string) (isNew bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	isNew = !c.failed || c.cause != cause
	c.failed, c.cause = true, cause

	return isNew
}

// Recovered records a success and reports whether a failure came before
func (c *CauseLog) Recovered() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	was := c.failed
	c.failed, c.cause = false, ""

	return was
}

// Report logs the result of one attempt: a new cause as warning, the same cause
// again at debug level, the first success after a failure once as info.
func (c *CauseLog) Report(log *Logger, what string, err error) {
	switch {
	case err == nil:
		if c.Recovered() {
			log.INFO.Printf("%s ok again", what)
		}
	case c.Failed(err.Error()):
		log.WARN.Printf("%s: %v", what, err)
	default:
		log.DEBUG.Printf("%s: %v", what, err)
	}
}
