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

// Over power is no ui notification, over current still is.
func TestOverPowerNotNotified(t *testing.T) {
	// the capture stays for the whole test binary: keep draining it
	var (
		mu  sync.Mutex
		ui  []string
		src = make(chan util.Param)
	)
	go func() {
		for p := range src {
			mu.Lock()
			ui = append(ui, fmt.Sprintf("%+v", p.Val))
			mu.Unlock()
		}
	}()
	util.CaptureLogs(src)

	notified := func() []string {
		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		res := ui
		ui = nil
		return res
	}

	log := util.NewLogger("overload")
	c, err := New(log, "peak", 16, 5000, nil, 0)
	require.NoError(t, err)
	notified()

	require.NoError(t, c.Update([]api.CircuitLoad{&testLoad{circuit: c, power: 7000, current: 10}}))
	assert.Empty(t, notified(), "over power only")

	require.NoError(t, c.Update([]api.CircuitLoad{&testLoad{circuit: c, power: 7000, current: 20}}))
	msgs := notified()
	require.Len(t, msgs, 1)
	assert.True(t, strings.Contains(msgs[0], "over current detected"), msgs[0])
}
