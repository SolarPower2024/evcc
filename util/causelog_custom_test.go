package util

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	jww "github.com/spf13/jwalterweatherman"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCauseLog(t *testing.T) {
	var c CauseLog

	assert.False(t, c.Recovered(), "nothing failed yet")

	assert.True(t, c.Failed("a"), "first failure")
	assert.False(t, c.Failed("a"), "same cause")
	assert.False(t, c.Failed("a"), "same cause again")
	assert.True(t, c.Failed("b"), "other cause")
	assert.False(t, c.Failed("b"))

	assert.True(t, c.Recovered())
	assert.False(t, c.Recovered(), "recovered once")

	// alternating: one new cause and one recovery per occurrence
	for range 3 {
		assert.True(t, c.Failed("a"))
		assert.True(t, c.Recovered())
	}
}

func TestCauseLogReport(t *testing.T) {
	var out bytes.Buffer
	l := &Logger{Notepad: jww.NewNotepad(jww.LevelTrace, jww.LevelTrace, &out, io.Discard, "", 0)}

	var c CauseLog
	e := errors.New("down")

	c.Report(l, "what", e)
	c.Report(l, "what", e)
	c.Report(l, "what", errors.New("other"))
	lines := func() []string { return strings.Split(strings.TrimSpace(out.String()), "\n") }

	require.Len(t, lines(), 3)
	assert.Contains(t, lines()[0], "WARN")
	assert.Contains(t, lines()[0], "what: down")
	assert.Contains(t, lines()[1], "DEBUG")
	assert.Contains(t, lines()[2], "WARN")
	assert.Contains(t, lines()[2], "what: other")

	c.Report(l, "what", nil)
	c.Report(l, "what", nil)

	require.Len(t, lines(), 4)
	assert.Contains(t, lines()[3], "INFO")
	assert.Contains(t, lines()[3], "what ok again")
}
