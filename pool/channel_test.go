package pool

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestChannelSafety(t *testing.T) {
	ch := NewChannel(1)

	// Initially safe
	assert.True(t, ch.IsSafe())

	// After Basic.Publish, still safe
	ch.RecordOperation("Basic.Publish")
	assert.True(t, ch.IsSafe())

	// After Basic.Consume, unsafe
	ch.RecordOperation("Basic.Consume")
	assert.False(t, ch.IsSafe())

	// Reset
	ch.Reset()
	assert.True(t, ch.IsSafe())
}
