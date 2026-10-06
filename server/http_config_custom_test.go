package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

type stagesDevice struct{}

func (stagesDevice) StageStates() ([]bool, error) {
	return []bool{true, true, false}, nil
}

// the config page shows each stage's switch of a heater in stages
func TestTestInstanceStages(t *testing.T) {
	res := testInstance(context.Background(), stagesDevice{})
	assert.Equal(t, []bool{true, true, false}, res["stages"].Value)
}
