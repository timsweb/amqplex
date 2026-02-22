package proxy

import (
	"bufio"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func newTestManagedUpstream(maxChannels uint16) *ManagedUpstream {
	return &ManagedUpstream{
		username:      "guest",
		password:      "guest",
		vhost:         "/",
		maxChannels:   maxChannels,
		usedChannels:  make(map[uint16]bool),
		channelOwners: make(map[uint16]channelEntry),
		clients:       make([]clientWriter, 0),
	}
}

// stubClient implements clientWriter for tests.
type stubClient struct {
	frames []*Frame
}

func (s *stubClient) DeliverFrame(f *Frame) error {
	s.frames = append(s.frames, f)
	return nil
}
func (s *stubClient) Abort() {}

func TestAllocateChannel_AssignsLowestFreeID(t *testing.T) {
	m := newTestManagedUpstream(65535)
	stub := &stubClient{}

	upstreamID, err := m.AllocateChannel(1, stub)
	assert.NoError(t, err)
	assert.Equal(t, uint16(1), upstreamID)

	upstreamID2, err := m.AllocateChannel(2, stub)
	assert.NoError(t, err)
	assert.Equal(t, uint16(2), upstreamID2)
}

func TestAllocateChannel_ReleasedChannelIsReused(t *testing.T) {
	m := newTestManagedUpstream(65535)
	stub := &stubClient{}

	id, _ := m.AllocateChannel(1, stub)
	assert.Equal(t, uint16(1), id)

	m.ReleaseChannel(id)

	id2, err := m.AllocateChannel(2, stub)
	assert.NoError(t, err)
	assert.Equal(t, uint16(1), id2) // reuses freed slot
}

func TestAllocateChannel_ErrorWhenFull(t *testing.T) {
	m := newTestManagedUpstream(2) // only 2 channels allowed
	stub := &stubClient{}

	_, err := m.AllocateChannel(1, stub)
	assert.NoError(t, err)
	_, err = m.AllocateChannel(2, stub)
	assert.NoError(t, err)
	_, err = m.AllocateChannel(3, stub)
	assert.ErrorContains(t, err, "no free upstream channel")
}

func TestAllocateChannel_ClientChannelIDStored(t *testing.T) {
	m := newTestManagedUpstream(65535)
	stub := &stubClient{}

	upstreamID, _ := m.AllocateChannel(7, stub)

	m.mu.Lock()
	entry := m.channelOwners[upstreamID]
	m.mu.Unlock()

	assert.Equal(t, uint16(7), entry.clientChanID)
	assert.Equal(t, clientWriter(stub), entry.owner)
}

func TestHasCapacity(t *testing.T) {
	m := newTestManagedUpstream(2)
	stub := &stubClient{}
	assert.True(t, m.HasCapacity())
	m.AllocateChannel(1, stub)
	m.AllocateChannel(2, stub)
	assert.False(t, m.HasCapacity())
}

// upstreamPipe creates a connected pair: proxyConn (what ManagedUpstream uses)
// and serverConn (what the test drives as the fake upstream).
func upstreamPipe(t *testing.T) (proxyConn net.Conn, serverConn net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		done <- c
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server := <-done
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

func startedUpstream(t *testing.T) (*ManagedUpstream, net.Conn) {
	t.Helper()
	proxyConn, serverConn := upstreamPipe(t)
	uc := &UpstreamConn{
		Conn:   proxyConn,
		Reader: bufio.NewReader(proxyConn),
		Writer: bufio.NewWriter(proxyConn),
	}
	m := newTestManagedUpstream(65535)
	m.Start(uc)
	return m, serverConn
}

func TestReadLoop_HeartbeatEchoed(t *testing.T) {
	m, server := startedUpstream(t)
	_ = m

	w := bufio.NewWriter(server)
	r := bufio.NewReader(server)

	// Send a heartbeat frame from the fake upstream
	hb := &Frame{Type: FrameTypeHeartbeat, Channel: 0, Payload: []byte{}}
	err := WriteFrame(w, hb)
	assert.NoError(t, err)
	assert.NoError(t, w.Flush())

	// Proxy must echo a heartbeat back
	server.SetReadDeadline(time.Now().Add(time.Second))
	echo, err := ParseFrame(r)
	assert.NoError(t, err)
	assert.Equal(t, FrameTypeHeartbeat, echo.Type)
}

func TestReadLoop_FrameDispatchedToClient(t *testing.T) {
	m, server := startedUpstream(t)
	stub := &stubClient{}

	// Register client as owner of upstream channel 5
	m.mu.Lock()
	m.usedChannels[5] = true
	m.channelOwners[5] = channelEntry{owner: stub, clientChanID: 1}
	m.clients = append(m.clients, stub)
	m.mu.Unlock()

	w := bufio.NewWriter(server)

	// Send a method frame on channel 5
	frame := &Frame{Type: FrameTypeMethod, Channel: 5, Payload: []byte{0, 60, 0, 40}}
	assert.NoError(t, WriteFrame(w, frame))
	assert.NoError(t, w.Flush())

	// Client should receive the frame remapped to client channel 1
	time.Sleep(50 * time.Millisecond)
	assert.Len(t, stub.frames, 1)
	assert.Equal(t, uint16(1), stub.frames[0].Channel)
}

func TestReadLoop_HeartbeatNotSentToClients(t *testing.T) {
	m, server := startedUpstream(t)
	stub := &stubClient{}
	m.Register(stub)

	w := bufio.NewWriter(server)
	hb := &Frame{Type: FrameTypeHeartbeat, Channel: 0, Payload: []byte{}}
	assert.NoError(t, WriteFrame(w, hb))
	assert.NoError(t, w.Flush())

	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, stub.frames, "heartbeat must not be forwarded to clients")
}
