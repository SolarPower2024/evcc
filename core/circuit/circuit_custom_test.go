package circuit

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testLoad struct {
	circuit        api.Circuit
	power, current float64
}

func (l *testLoad) GetChargePower() float64     { return l.power }
func (l *testLoad) GetMaxPhaseCurrent() float64 { return l.current }
func (l *testLoad) GetCircuit() api.Circuit     { return l.circuit }

var (
	captureOnce sync.Once
	uiMsgs      = make(chan string, 100)
)

// captureUi starts the capture of ui messages once per test binary, as it
// stays for the whole process
func captureUi() {
	captureOnce.Do(func() {
		src := make(chan util.Param)
		go func() {
			for p := range src {
				// other tests log warnings of their own too
				msg := fmt.Sprintf("%+v", p.Val)
				if !strings.Contains(msg, "over ") {
					continue
				}
				select {
				case uiMsgs <- msg:
				default:
				}
			}
		}()
		util.CaptureLogs(src)
	})
}

// Over power is no ui notification, over current still is. Messages of other
// tests arrive late (also from a run before), so only the ones of this circuit
// count: 7000 W and 20 A.
func TestOverPowerNotNotified(t *testing.T) {
	captureUi()

	// seen reports whether a message with sub arrives within wait
	seen := func(sub string, wait time.Duration) bool {
		timeout := time.After(wait)
		for {
			select {
			case m := <-uiMsgs:
				if strings.Contains(m, sub) {
					return true
				}
			case <-timeout:
				return false
			}
		}
	}

	c, err := New(util.NewLogger("overload"), "peak", 16, 5000, nil, 0)
	require.NoError(t, err)

	require.NoError(t, c.Update([]api.CircuitLoad{&testLoad{circuit: c, power: 7000, current: 10}}))
	assert.False(t, seen("7000W", 150*time.Millisecond), "over power only")

	require.NoError(t, c.Update([]api.CircuitLoad{&testLoad{circuit: c, power: 7000, current: 20}}))
	assert.True(t, seen("over current detected: 20A", 5*time.Second), "no message for over current")
	assert.False(t, seen("7000W", 100*time.Millisecond), "no message for over power")
}
