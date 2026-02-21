package proxy

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestProxyClientToUpstream(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	fp := NewFrameProxy(clientConn)

	frame := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{10, 40}}
	err := fp.ProxyClientToUpstream(frame)
	assert.NoError(t, err)
}

func TestProxyClientToUpstreamWithMapping(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	fp := NewFrameProxy(clientConn)

	// Map client channel 1 to upstream channel 100
	clientConn.MapChannel(1, 100)

	frame := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{10, 40}}
	err := fp.ProxyClientToUpstream(frame)
	assert.NoError(t, err)
}

func TestProxyUpstreamToClientWithMapping(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	fp := NewFrameProxy(clientConn)

	// Map client channel 1 to upstream channel 100
	clientConn.MapChannel(1, 100)

	// Test reverse lookup: upstream 100 should map to client 1
	frame := &Frame{Type: FrameTypeMethod, Channel: 100, Payload: []byte{10, 40}}
	err := fp.ProxyUpstreamToClient(frame)
	assert.NoError(t, err)
}

func TestProxyUpstreamToClientNoMapping(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	fp := NewFrameProxy(clientConn)

	// No mapping exists for upstream channel 100
	frame := &Frame{Type: FrameTypeMethod, Channel: 100, Payload: []byte{10, 40}}
	err := fp.ProxyUpstreamToClient(frame)
	// Should return nil (no mapping)
	assert.NoError(t, err)
}

func TestFrameProxyReset(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	fp := NewFrameProxy(clientConn)

	// Write some data
	fp.mu.Lock()
	fp.Writer.WriteString("test data")
	fp.mu.Unlock()

	// Reset should clear buffer
	fp.Reset()

	fp.mu.Lock()
	assert.Equal(t, 0, fp.Writer.Len())
	fp.mu.Unlock()
}

func TestProxyClientToUpstreamChannelZero(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	fp := NewFrameProxy(clientConn)

	// Channel 0 is valid for connection-level methods
	clientConn.MapChannel(0, 100)

	frame := &Frame{Type: FrameTypeMethod, Channel: 0, Payload: []byte{10, 10}}
	err := fp.ProxyClientToUpstream(frame)
	assert.NoError(t, err)
}
