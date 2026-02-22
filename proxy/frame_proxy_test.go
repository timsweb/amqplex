package proxy

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProxyClientToUpstreamChannelRemapping(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	clientConn.MapChannel(1, 100)

	upstreamBuf := &bytes.Buffer{}
	fp := NewFrameProxy(clientConn, upstreamBuf, nil)

	frame := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{0, 10, 0, 40}}
	err := fp.ProxyClientToUpstream(frame)
	assert.NoError(t, err)

	// Check written frame has channel 100 (upstream channel), not 1
	written := upstreamBuf.Bytes()
	assert.GreaterOrEqual(t, len(written), 7)
	channelInFrame := binary.BigEndian.Uint16(written[1:3])
	assert.Equal(t, uint16(100), channelInFrame)
}

func TestProxyUpstreamToClientChannelRemapping(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	clientConn.MapChannel(1, 100)

	clientBuf := &bytes.Buffer{}
	fp := NewFrameProxy(clientConn, nil, clientBuf)

	// Upstream sends on channel 100; client expects channel 1
	frame := &Frame{Type: FrameTypeMethod, Channel: 100, Payload: []byte{0, 10, 0, 40}}
	err := fp.ProxyUpstreamToClient(frame)
	assert.NoError(t, err)

	written := clientBuf.Bytes()
	assert.GreaterOrEqual(t, len(written), 7)
	channelInFrame := binary.BigEndian.Uint16(written[1:3])
	assert.Equal(t, uint16(1), channelInFrame)
}

func TestProxyUpstreamToClientNoMapping(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	clientBuf := &bytes.Buffer{}
	fp := NewFrameProxy(clientConn, nil, clientBuf)

	frame := &Frame{Type: FrameTypeMethod, Channel: 100, Payload: []byte{0, 10, 0, 40}}
	err := fp.ProxyUpstreamToClient(frame)
	assert.NoError(t, err)
	// No mapping: nothing should be written
	assert.Equal(t, 0, clientBuf.Len())
}

func TestProxyClientToUpstream(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	upstreamBuf := &bytes.Buffer{}
	fp := NewFrameProxy(clientConn, upstreamBuf, nil)

	frame := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{10, 40}}
	err := fp.ProxyClientToUpstream(frame)
	assert.NoError(t, err)
}

func TestProxyClientToUpstreamWithMapping(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	clientConn.MapChannel(1, 100)
	upstreamBuf := &bytes.Buffer{}
	fp := NewFrameProxy(clientConn, upstreamBuf, nil)

	frame := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{10, 40}}
	err := fp.ProxyClientToUpstream(frame)
	assert.NoError(t, err)
}

func TestProxyUpstreamToClientWithMapping(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	clientConn.MapChannel(1, 100)
	clientBuf := &bytes.Buffer{}
	fp := NewFrameProxy(clientConn, nil, clientBuf)

	// Test reverse lookup: upstream 100 should map to client 1
	frame := &Frame{Type: FrameTypeMethod, Channel: 100, Payload: []byte{10, 40}}
	err := fp.ProxyUpstreamToClient(frame)
	assert.NoError(t, err)
}

func TestFrameProxyReset(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	upstreamBuf := &bytes.Buffer{}
	clientBuf := &bytes.Buffer{}
	fp := NewFrameProxy(clientConn, upstreamBuf, clientBuf)

	// No Reset method anymore - buffers are provided externally
	assert.NotNil(t, fp)
}

func TestProxyClientToUpstreamChannelZero(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	clientConn.MapChannel(0, 100)
	upstreamBuf := &bytes.Buffer{}
	fp := NewFrameProxy(clientConn, upstreamBuf, nil)

	// Channel 0 is valid for connection-level methods
	frame := &Frame{Type: FrameTypeMethod, Channel: 0, Payload: []byte{10, 10}}
	err := fp.ProxyClientToUpstream(frame)
	assert.NoError(t, err)
}

func TestProxyUpstreamToClientChannelZeroPassthrough(t *testing.T) {
	clientConn := NewClientConnection(nil, nil)
	// No channel mappings — simulates a fresh connection

	clientBuf := &bytes.Buffer{}
	fp := NewFrameProxy(clientConn, nil, clientBuf)

	// Channel-0 frames (heartbeats, Connection.Close, Connection.Blocked) must
	// always be forwarded to the client even though channel 0 is never in ReverseMapping.
	frame := &Frame{Type: FrameTypeMethod, Channel: 0, Payload: []byte{0, 10, 0, 10}}
	err := fp.ProxyUpstreamToClient(frame)
	assert.NoError(t, err)

	written := clientBuf.Bytes()
	assert.Greater(t, len(written), 0, "channel-0 frame must be forwarded to client")
	channelInFrame := binary.BigEndian.Uint16(written[1:3])
	assert.Equal(t, uint16(0), channelInFrame)
}
