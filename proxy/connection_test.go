package proxy

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewClientConnection(t *testing.T) {
	conn := &net.TCPConn{}
	proxy := &Proxy{}

	clientConn := NewClientConnection(conn, proxy)
	assert.NotNil(t, clientConn)
	assert.Equal(t, conn, clientConn.Conn)
}

func TestSerializeConnectionStart(t *testing.T) {
	data := serializeConnectionStart()

	// Must have at least: 4 (method header) + 1 + 1 + 4 (empty table) + 4+5 ("PLAIN") + 4+5 ("en_US") = 28 bytes
	assert.GreaterOrEqual(t, len(data), 28)

	// Method header: class=10, method=10
	assert.Equal(t, uint16(10), binary.BigEndian.Uint16(data[0:2]))
	assert.Equal(t, uint16(10), binary.BigEndian.Uint16(data[2:4]))

	// Version: 0, 9
	assert.Equal(t, byte(0), data[4])
	assert.Equal(t, byte(9), data[5])

	// server-properties table: 4-byte length = 0 (empty table)
	assert.Equal(t, uint32(0), binary.BigEndian.Uint32(data[6:10]))

	// mechanisms longstr: length=5, content="PLAIN"
	assert.Equal(t, uint32(5), binary.BigEndian.Uint32(data[10:14]))
	assert.Equal(t, "PLAIN", string(data[14:19]))

	// locales longstr: length=5, content="en_US"
	assert.Equal(t, uint32(5), binary.BigEndian.Uint32(data[19:23]))
	assert.Equal(t, "en_US", string(data[23:28]))
}

func TestSendConnectionTunePayload(t *testing.T) {
	// serializeConnectionTunePayload should produce the correct 12-byte body
	data := serializeConnectionTunePayload()

	// 4 (method header) + 2 (channel-max) + 4 (frame-max) + 2 (heartbeat) = 12 bytes
	assert.Equal(t, 12, len(data))

	// Method header: class=10, method=30
	assert.Equal(t, uint16(10), binary.BigEndian.Uint16(data[0:2]))
	assert.Equal(t, uint16(30), binary.BigEndian.Uint16(data[2:4]))

	// channel-max = 0 (no limit)
	assert.Equal(t, uint16(0), binary.BigEndian.Uint16(data[4:6]))

	// frame-max = 131072 (128KB, standard RabbitMQ default)
	assert.Equal(t, uint32(131072), binary.BigEndian.Uint32(data[6:10]))

	// heartbeat = 60 as uint16
	assert.Equal(t, uint16(60), binary.BigEndian.Uint16(data[10:12]))
}

func TestClientConnectionDeliverFrame(t *testing.T) {
	buf := &bytes.Buffer{}
	cc := NewClientConnection(nil, nil)
	cc.Writer = bufio.NewWriter(buf)

	frame := &Frame{Type: FrameTypeMethod, Channel: 5, Payload: []byte{0, 20, 0, 11}}
	err := cc.DeliverFrame(frame)
	assert.NoError(t, err)
	assert.Greater(t, buf.Len(), 0)

	// Verify channel 5 is in the written frame bytes
	written := buf.Bytes()
	channelInFrame := binary.BigEndian.Uint16(written[1:3])
	assert.Equal(t, uint16(5), channelInFrame)
}

func TestUnmapChannelCleansClientChannels(t *testing.T) {
	cc := NewClientConnection(nil, nil)
	cc.MapChannel(1, 100)

	// Verify channel exists
	cc.Mu.RLock()
	_, exists := cc.ClientChannels[1]
	cc.Mu.RUnlock()
	assert.True(t, exists)

	cc.UnmapChannel(1)

	// After unmap, ClientChannels must also be cleaned
	cc.Mu.RLock()
	_, exists = cc.ClientChannels[1]
	cc.Mu.RUnlock()
	assert.False(t, exists)

	// ChannelMapping and ReverseMapping must also be gone
	cc.Mu.RLock()
	_, inMapping := cc.ChannelMapping[1]
	_, inReverse := cc.ReverseMapping[100]
	cc.Mu.RUnlock()
	assert.False(t, inMapping)
	assert.False(t, inReverse)
}
