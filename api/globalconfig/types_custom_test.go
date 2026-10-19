package globalconfig

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

// the second feed-in tariff alone makes the tariff references configured and
// used: its device is kept when it is the only reference
func TestTariffRefsFeedInEeg(t *testing.T) {
	refs := TariffRefs{FeedInEeg: "eeg"}

	assert.True(t, refs.IsConfigured())
	assert.Equal(t, []string{"eeg"}, slices.Collect(refs.Used()))

	assert.False(t, TariffRefs{}.IsConfigured())
	assert.Empty(t, slices.Collect(TariffRefs{}.Used()))
}
