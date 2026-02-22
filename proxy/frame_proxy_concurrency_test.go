package proxy

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFrameProxyConcurrency(t *testing.T) {
	cc := NewClientConnection(nil, nil)
	cc.MapChannel(1, 100)
	cc.MapChannel(2, 200)

	upstreamBuf := &bytes.Buffer{}
	clientBuf := &bytes.Buffer{}
	fp := NewFrameProxy(cc, upstreamBuf, clientBuf)

	var ops uint64
	var wg sync.WaitGroup

	// Concurrently proxy 1000 frames in each direction
	numOps := 1000
	for i := 0; i < numOps; i++ {
		wg.Add(2)

		// Client → Upstream
		go func(op int) {
			defer wg.Done()
			frame := &Frame{Type: FrameTypeMethod, Channel: 1 + uint16(op%2), Payload: []byte{1, 2, 3}}
			_ = fp.ProxyClientToUpstream(frame)
			atomic.AddUint64(&ops, 1)
		}(i)

		// Upstream → Client
		go func(op int) {
			defer wg.Done()
			frame := &Frame{Type: FrameTypeMethod, Channel: 100 + uint16(op%2), Payload: []byte{1, 2, 3}}
			_ = fp.ProxyUpstreamToClient(frame)
			atomic.AddUint64(&ops, 1)
		}(i)
	}
	wg.Wait()

	// All operations completed without panic
	assert.Equal(t, uint64(numOps*2), atomic.LoadUint64(&ops))
}

func TestFrameProxyNilWriter(t *testing.T) {
	cc := NewClientConnection(nil, nil)
	cc.MapChannel(1, 100)

	// FrameProxy with nil writers must not panic — nil is treated as a no-op.
	fp := NewFrameProxy(cc, nil, nil)

	clientFrame := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{1, 2, 3}}
	err := fp.ProxyClientToUpstream(clientFrame)
	assert.NoError(t, err)

	upstreamFrame := &Frame{Type: FrameTypeMethod, Channel: 100, Payload: []byte{1, 2, 3}}
	err = fp.ProxyUpstreamToClient(upstreamFrame)
	assert.NoError(t, err)
}

func TestFrameProxyChannelRemappingUnderLoad(t *testing.T) {
	cc := NewClientConnection(nil, nil)
	var wg sync.WaitGroup

	// Concurrently map and unmap channels
	numIterations := 100
	for i := 0; i < numIterations; i++ {
		wg.Add(3)

		go func(i int) {
			defer wg.Done()
			clientID := uint16(i % 10)
			upstreamID := uint16(i + 100)
			cc.MapChannel(clientID, upstreamID)
		}(i)

		go func(i int) {
			defer wg.Done()
			clientID := uint16(i % 10)
			cc.UnmapChannel(clientID)
		}(i)

		go func(i int) {
			defer wg.Done()
			clientID := uint16(i % 10)
			cc.Mu.RLock()
			_ = cc.ChannelMapping[clientID]
			cc.Mu.RUnlock()
		}(i)
	}
	wg.Wait()

	// Should complete without deadlock or panic
}
