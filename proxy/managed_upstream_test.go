package proxy

import (
	"bufio"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestManagedUpstream(maxChannels uint16) *ManagedUpstream {
	return &ManagedUpstream{
		username:      "guest",
		password:      "guest",
		vhost:         "/",
		maxChannels:   maxChannels,
		usedChannels:  make(map[uint16]bool),
		channelOwners: make(map[uint16]channelEntry),
		pendingClose:  make(map[uint16]bool),
		clients:       make([]clientWriter, 0),
		dialFn:        func() (*UpstreamConn, error) { return nil, errors.New("no dial fn") },
	}
}

// stubClient implements clientWriter for tests.
type stubClient struct {
	frames    []*Frame
	delivered chan struct{} // signalled when DeliverFrame is called
}

func newStubClient() *stubClient {
	return &stubClient{delivered: make(chan struct{}, 10)}
}

func (s *stubClient) DeliverFrame(f *Frame) error {
	s.frames = append(s.frames, f)
	if s.delivered != nil {
		select {
		case s.delivered <- struct{}{}:
		default:
		}
	}
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
	t.Cleanup(func() { m.stopped.Store(true) })
	return m, serverConn
}

func TestReconnectLoop_RestartsReadLoop(t *testing.T) {
	proxyConn, serverConn := upstreamPipe(t)
	uc := &UpstreamConn{
		Conn:   proxyConn,
		Reader: bufio.NewReader(proxyConn),
		Writer: bufio.NewWriter(proxyConn),
	}

	proxyConn2, _ := upstreamPipe(t)

	dialCount := 0
	reconnected := make(chan struct{}, 1)

	m := newTestManagedUpstream(65535)
	m.dialFn = func() (*UpstreamConn, error) {
		dialCount++
		reconnected <- struct{}{}
		return &UpstreamConn{
			Conn:   proxyConn2,
			Reader: bufio.NewReader(proxyConn2),
			Writer: bufio.NewWriter(proxyConn2),
		}, nil
	}
	m.reconnectBase = 10 * time.Millisecond // fast reconnect for tests
	m.Start(uc)

	// Kill the upstream connection to trigger reconnect
	serverConn.Close()

	// Wait for reconnect
	select {
	case <-reconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: reconnect did not happen")
	}
	assert.Equal(t, 1, dialCount, "should have dialled once to reconnect")
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
	stub := newStubClient()

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
	select {
	case <-stub.delivered:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for frame delivery")
	}
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

	// Wait for the echo to arrive on the server side — proves the read loop
	// processed the heartbeat before we assert the stub received nothing.
	r := bufio.NewReader(server)
	server.SetReadDeadline(time.Now().Add(time.Second))
	echo, err := ParseFrame(r)
	assert.NoError(t, err)
	assert.Equal(t, FrameTypeHeartbeat, echo.Type)
	assert.Empty(t, stub.frames, "heartbeat must not be forwarded to clients")
}

func TestManagedUpstreamLogsConnected(t *testing.T) {
	lc, logger := newCapture()

	proxyConn, _ := upstreamPipe(t)
	uc := &UpstreamConn{
		Conn:   proxyConn,
		Reader: bufio.NewReader(proxyConn),
		Writer: bufio.NewWriter(proxyConn),
	}

	m := &ManagedUpstream{
		username:      "user",
		password:      "pass",
		vhost:         "/",
		maxChannels:   10,
		usedChannels:  make(map[uint16]bool),
		channelOwners: make(map[uint16]channelEntry),
		clients:       make([]clientWriter, 0),
		upstreamAddr:  "localhost:5672",
		logger:        logger,
	}
	m.dialFn = func() (*UpstreamConn, error) { return nil, nil }

	m.Start(uc)
	defer m.stopped.Store(true)

	require.True(t, lc.waitForMessage("upstream connected", 200*time.Millisecond), "expected 'upstream connected' log")
	val, ok := lc.attrValue("upstream_addr")
	assert.True(t, ok)
	assert.Equal(t, "localhost:5672", val.String())
}

func TestAllocateReleaseChannelLogged(t *testing.T) {
	lc, logger := newCapture()

	m := &ManagedUpstream{
		username:      "user",
		password:      "pass",
		vhost:         "/",
		maxChannels:   10,
		usedChannels:  make(map[uint16]bool),
		channelOwners: make(map[uint16]channelEntry),
		clients:       make([]clientWriter, 0),
		logger:        logger,
		upstreamAddr:  "localhost:5672",
	}

	client := newStubClient()
	upstreamID, err := m.AllocateChannel(1, client)
	require.NoError(t, err)
	assert.Contains(t, lc.messages(), "channel allocated")
	upstreamChanVal, ok := lc.attrValue("upstream_chan")
	assert.True(t, ok, "expected upstream_chan field in 'channel allocated' log")
	assert.Equal(t, int64(upstreamID), upstreamChanVal.Int64())

	m.ReleaseChannel(upstreamID)
	assert.Contains(t, lc.messages(), "channel released")
}

func TestMarkStoppedIfIdle_NotIdleWhenClientsPresent(t *testing.T) {
	m := newTestManagedUpstream(10)
	m.lastEmptyTime = time.Time{} // zero = has clients
	assert.False(t, m.markStoppedIfIdle(0))
	assert.False(t, m.stopped.Load())
}

func TestMarkStoppedIfIdle_NotIdleBeforeTimeout(t *testing.T) {
	m := newTestManagedUpstream(10)
	m.lastEmptyTime = time.Now() // just became empty
	assert.False(t, m.markStoppedIfIdle(time.Hour))
	assert.False(t, m.stopped.Load())
}

func TestMarkStoppedIfIdle_StopsWhenTimedOut(t *testing.T) {
	m := newTestManagedUpstream(10)
	m.lastEmptyTime = time.Now().Add(-2 * time.Second)
	assert.True(t, m.markStoppedIfIdle(time.Second))
	assert.True(t, m.stopped.Load())
}

func TestDeregisterSetsLastEmptyTime(t *testing.T) {
	m := newTestManagedUpstream(10)
	client := newStubClient()
	m.Register(client)
	assert.True(t, m.lastEmptyTime.IsZero())

	before := time.Now()
	m.Deregister(client)
	assert.False(t, m.lastEmptyTime.IsZero())
	assert.True(t, m.lastEmptyTime.After(before) || m.lastEmptyTime.Equal(before))
}

func TestDeregisterUnknownClientDoesNotSetLastEmptyTime(t *testing.T) {
	m := newTestManagedUpstream(10)
	realClient := newStubClient()
	m.Register(realClient)
	// Deregister a client that was never registered
	m.Deregister(newStubClient())
	// lastEmptyTime must remain zero — realClient is still registered
	assert.True(t, m.lastEmptyTime.IsZero(), "lastEmptyTime must not be set for an unregistered client")
}

func TestRegisterResetsLastEmptyTime(t *testing.T) {
	m := newTestManagedUpstream(10)
	m.lastEmptyTime = time.Now().Add(-time.Hour)
	client := newStubClient()
	m.Register(client)
	assert.True(t, m.lastEmptyTime.IsZero())
}

func TestReconnectTotalIncrements(t *testing.T) {
	m := newTestManagedUpstream(10)
	m.reconnectBase = time.Millisecond

	var calls atomic.Int32
	m.dialFn = func() (*UpstreamConn, error) {
		if calls.Add(1) >= 3 {
			m.stopped.Store(true)
		}
		return nil, errors.New("dial failed")
	}

	m.reconnectLoop()
	assert.GreaterOrEqual(t, m.reconnectTotal.Load(), int64(2))
}

func TestManagedUpstreamLogsReconnect(t *testing.T) {
	lc, logger := newCapture()

	var attempts int32
	m := &ManagedUpstream{
		username:      "user",
		password:      "pass",
		vhost:         "/",
		maxChannels:   10,
		usedChannels:  make(map[uint16]bool),
		channelOwners: make(map[uint16]channelEntry),
		clients:       make([]clientWriter, 0),
		reconnectBase: 10 * time.Millisecond,
		upstreamAddr:  "localhost:5672",
		logger:        logger,
	}

	proxyConn2, _ := upstreamPipe(t)

	m.dialFn = func() (*UpstreamConn, error) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 2 {
			return nil, errors.New("connection refused")
		}
		return &UpstreamConn{
			Conn:   proxyConn2,
			Reader: bufio.NewReader(proxyConn2),
			Writer: bufio.NewWriter(proxyConn2),
		}, nil
	}

	m.handleUpstreamFailure(errors.New("read: connection reset by peer"))

	assert.True(t, lc.waitForMessage("upstream lost", 500*time.Millisecond))
	assert.True(t, lc.waitForMessage("upstream reconnecting", 500*time.Millisecond))
	assert.True(t, lc.waitForMessage("upstream reconnected", 500*time.Millisecond))
}
