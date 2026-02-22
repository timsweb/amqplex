package proxy

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConcurrentChannelMapping(t *testing.T) {
	cc := NewClientConnection(nil, nil)
	var wg sync.WaitGroup

	// Concurrently map 100 channels
	numChannels := 100
	for i := 0; i < numChannels; i++ {
		wg.Add(1)
		go func(clientID, upstreamID uint16) {
			defer wg.Done()
			cc.MapChannel(clientID, upstreamID)
		}(uint16(i), uint16(i+100))
	}
	wg.Wait()

	// Verify all mappings exist
	cc.Mu.RLock()
	assert.Equal(t, numChannels, len(cc.ChannelMapping))
	assert.Equal(t, numChannels, len(cc.ReverseMapping))
	cc.Mu.RUnlock()

	// Concurrently unmap all channels
	for i := 0; i < numChannels; i++ {
		wg.Add(1)
		go func(clientID uint16) {
			defer wg.Done()
			cc.UnmapChannel(clientID)
		}(uint16(i))
	}
	wg.Wait()

	// Verify all mappings are cleaned
	cc.Mu.RLock()
	assert.Equal(t, 0, len(cc.ChannelMapping))
	assert.Equal(t, 0, len(cc.ReverseMapping))
	assert.Equal(t, 0, len(cc.ClientChannels))
	cc.Mu.RUnlock()
}

func TestChannelCloseAndReuse(t *testing.T) {
	cc := NewClientConnection(nil, nil)

	// Map channel 1
	cc.MapChannel(1, 100)
	cc.Mu.RLock()
	_, exists := cc.ChannelMapping[1]
	assert.True(t, exists)
	cc.Mu.RUnlock()

	// Unmap channel
	cc.UnmapChannel(1)
	cc.Mu.RLock()
	_, exists = cc.ChannelMapping[1]
	assert.False(t, exists)
	cc.Mu.RUnlock()

	// Reuse channel 1 with different upstream ID
	cc.MapChannel(1, 200)
	cc.Mu.RLock()
	upstreamID, exists := cc.ChannelMapping[1]
	require.True(t, exists)
	assert.Equal(t, uint16(200), upstreamID)
	cc.Mu.RUnlock()
}

func TestInvalidChannelHandling(t *testing.T) {
	cc := NewClientConnection(nil, nil)

	// Try to unmap non-existent channel - should not panic
	cc.UnmapChannel(999)
	cc.Mu.RLock()
	_, exists := cc.ChannelMapping[999]
	assert.False(t, exists)
	cc.Mu.RUnlock()
}
