# ManagedUpstream Multiplexing Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the per-client upstream dial with a shared `ManagedUpstream` that multiplexes multiple clients onto one upstream AMQP connection, with reconnection, heartbeat proxying, and graceful shutdown drain.

**Architecture:** Introduce `proxy.ManagedUpstream` — one per `(username, password, vhost)` — that owns the upstream TCP connection, allocates channel IDs (packing strategy), dispatches upstream frames to client writers, echoes heartbeats, and reconnects on failure. `handleConnection` registers with `ManagedUpstream` and runs only the client→upstream direction. `Proxy` tracks active connections with a `sync.WaitGroup` for 30-second drain on `Stop()`.

**Tech Stack:** Go 1.22+, standard library (`net`, `crypto/tls`, `bufio`, `sync`, `sync/atomic`, `time`, `os/signal`), `github.com/stretchr/testify/assert` for tests.

**Design doc:** `docs/plans/2026-02-22-multiplexing-design.md`

---

## Background: AMQP Channel Methods Relevant to This Plan

```
Channel.Open    class=20, method=10  — client opens a channel
Channel.OpenOk  class=20, method=11  — server confirms channel open
Channel.Close   class=20, method=40  — either side closes a channel
Channel.CloseOk class=20, method=41  — confirms channel close

Basic.Consume   class=60, method=20  — marks channel UNSAFE (has subscriber)
Basic.Qos       class=60, method=10  — marks channel UNSAFE
Basic.Ack       class=60, method=80  — marks channel UNSAFE
Basic.Reject    class=60, method=90  — marks channel UNSAFE
Basic.Nack      class=60, method=120 — marks channel UNSAFE

Heartbeat frame: Type=8 (not a method frame — no class/method)
```

Channels that have only seen `Basic.Publish` (class=60, method=40) or `Basic.Get` (class=60, method=70) are **safe** to silently reuse. All others must be explicitly closed on the upstream when the client disconnects.

---

## Task 1: Add `DeliverFrame` and `Abort` to `ClientConnection`

`ManagedUpstream`'s read loop needs to write frames to clients. `ClientConnection` must expose a thread-safe write method and a way to force-close.

**Files:**
- Modify: `proxy/connection.go`
- Modify: `proxy/connection_test.go`

**Step 1: Write the failing test**

Add to `proxy/connection_test.go`:

```go
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
```

Add `"bufio"` and `"encoding/binary"` to imports if not already present.

**Step 2: Run to confirm it fails**

```bash
go test ./proxy/... -run TestClientConnectionDeliverFrame -v
```

Expected: FAIL — `DeliverFrame not defined`

**Step 3: Implement**

Add to `proxy/connection.go` (add `writerMu sync.Mutex` field to the struct, then the methods):

```go
type ClientConnection struct {
	Conn           net.Conn
	ClientChannels map[uint16]*ClientChannel
	ChannelMapping map[uint16]uint16
	ReverseMapping map[uint16]uint16
	Mu             sync.RWMutex
	writerMu       sync.Mutex  // serialises DeliverFrame calls from ManagedUpstream
	Proxy          *Proxy
	Reader         *bufio.Reader
	Writer         *bufio.Writer
	Pool           *pool.ConnectionPool
}
```

Add methods after the struct:

```go
// DeliverFrame writes a frame to the client connection. Thread-safe; may be
// called concurrently by ManagedUpstream's read loop.
func (cc *ClientConnection) DeliverFrame(frame *Frame) error {
	cc.writerMu.Lock()
	defer cc.writerMu.Unlock()
	if err := WriteFrame(cc.Writer, frame); err != nil {
		return err
	}
	return cc.Writer.Flush()
}

// Abort forcibly closes the underlying network connection, causing any
// in-progress reads or writes to return immediately with an error.
func (cc *ClientConnection) Abort() {
	if cc.Conn != nil {
		cc.Conn.Close()
	}
}
```

**Step 4: Run to confirm it passes**

```bash
go test ./proxy/... -run TestClientConnectionDeliverFrame -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add proxy/connection.go proxy/connection_test.go
git commit -m "feat: add DeliverFrame and Abort to ClientConnection"
```

---

## Task 2: `ManagedUpstream` — Struct and Channel Allocation

Create the `ManagedUpstream` type with channel allocation logic. No networking yet.

**Files:**
- Create: `proxy/managed_upstream.go`
- Create: `proxy/managed_upstream_test.go`

**Step 1: Write failing tests**

Create `proxy/managed_upstream_test.go`:

```go
package proxy

import (
	"testing"

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
```

**Step 2: Run to confirm they fail**

```bash
go test ./proxy/... -run "TestAllocateChannel|TestHasCapacity" -v
```

Expected: FAIL — `ManagedUpstream` not defined

**Step 3: Create `proxy/managed_upstream.go`**

```go
package proxy

import (
	"errors"
	"sync"
	"sync/atomic"
)

// clientWriter is the interface ManagedUpstream uses to interact with a
// registered client connection. ClientConnection implements this.
type clientWriter interface {
	DeliverFrame(frame *Frame) error
	Abort()
}

// channelEntry binds an upstream channel ID to the client that owns it and
// the client-side channel ID used for remapping.
type channelEntry struct {
	owner        clientWriter
	clientChanID uint16
}

// ManagedUpstream owns one upstream AMQP connection shared by multiple clients.
// One instance exists per (username, password, vhost) credential set.
type ManagedUpstream struct {
	username, password, vhost string
	maxChannels               uint16

	// dialFn dials a raw net.Conn to the upstream. Injected for testability.
	dialFn func() (interface{ Close() error }, error)

	mu            sync.Mutex
	conn          *UpstreamConn
	usedChannels  map[uint16]bool
	channelOwners map[uint16]channelEntry
	clients       []clientWriter

	stopped   atomic.Bool
	heartbeat uint16 // negotiated heartbeat interval in seconds
}

// AllocateChannel finds the lowest free upstream channel ID, registers the
// mapping, and returns the upstream ID. Returns an error if MaxChannels is
// exhausted.
func (m *ManagedUpstream) AllocateChannel(clientChanID uint16, cw clientWriter) (uint16, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id := uint16(1); id <= m.maxChannels; id++ {
		if !m.usedChannels[id] {
			m.usedChannels[id] = true
			m.channelOwners[id] = channelEntry{owner: cw, clientChanID: clientChanID}
			return id, nil
		}
	}
	return 0, errors.New("no free upstream channel available")
}

// ReleaseChannel marks an upstream channel ID as free.
func (m *ManagedUpstream) ReleaseChannel(upstreamChanID uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.usedChannels, upstreamChanID)
	delete(m.channelOwners, upstreamChanID)
}

// HasCapacity reports whether this upstream has at least one free channel slot.
func (m *ManagedUpstream) HasCapacity() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return uint16(len(m.usedChannels)) < m.maxChannels
}

// Register adds a client to the teardown list.
func (m *ManagedUpstream) Register(cw clientWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients = append(m.clients, cw)
}

// Deregister removes a client from the teardown list.
func (m *ManagedUpstream) Deregister(cw clientWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, c := range m.clients {
		if c == cw {
			m.clients = append(m.clients[:i], m.clients[i+1:]...)
			return
		}
	}
}
```

**Step 4: Run to confirm tests pass**

```bash
go test ./proxy/... -run "TestAllocateChannel|TestHasCapacity" -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add proxy/managed_upstream.go proxy/managed_upstream_test.go
git commit -m "feat: add ManagedUpstream with channel allocation"
```

---

## Task 3: `ManagedUpstream` — Read Loop, Heartbeat, Frame Dispatch

Add `Start()` which launches the goroutine that reads from the upstream, echoes heartbeats, and dispatches frames to clients.

**Files:**
- Modify: `proxy/managed_upstream.go`
- Modify: `proxy/managed_upstream_test.go`

**Step 1: Write failing tests**

Add to `proxy/managed_upstream_test.go`:

```go
import (
	"bufio"
	"net"
	"testing"
	"time"
)

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
```

**Step 2: Run to confirm they fail**

```bash
go test ./proxy/... -run "TestReadLoop" -v
```

Expected: FAIL — `Start` not defined / compilation error

**Step 3: Implement `Start` and `readLoop`**

Add to `proxy/managed_upstream.go`:

```go
// Start sets the upstream connection and launches the read loop goroutine.
// Must be called exactly once after creation.
func (m *ManagedUpstream) Start(conn *UpstreamConn) {
	m.mu.Lock()
	m.conn = conn
	m.mu.Unlock()
	go m.readLoop()
}

func (m *ManagedUpstream) readLoop() {
	for {
		m.mu.Lock()
		conn := m.conn
		m.mu.Unlock()
		if conn == nil {
			return
		}

		frame, err := ParseFrame(conn.Reader)
		if err != nil {
			if !m.stopped.Load() {
				m.handleUpstreamFailure()
			}
			return
		}

		switch {
		case frame.Type == FrameTypeHeartbeat:
			// Echo heartbeat back to upstream; do not forward to clients.
			m.mu.Lock()
			c := m.conn
			m.mu.Unlock()
			if c != nil {
				hb := &Frame{Type: FrameTypeHeartbeat, Channel: 0, Payload: []byte{}}
				_ = WriteFrame(c.Writer, hb)
				_ = c.Writer.Flush()
			}

		case frame.Channel == 0:
			// Connection-level frame (e.g. Connection.Close from upstream).
			// Forward to all registered clients and abort them.
			m.mu.Lock()
			clients := append([]clientWriter(nil), m.clients...)
			m.mu.Unlock()
			for _, cw := range clients {
				_ = cw.DeliverFrame(frame)
				cw.Abort()
			}

		default:
			// Remap channel and dispatch to the owning client.
			m.mu.Lock()
			entry, ok := m.channelOwners[frame.Channel]
			m.mu.Unlock()
			if !ok {
				continue
			}
			remapped := *frame
			remapped.Channel = entry.clientChanID
			_ = entry.owner.DeliverFrame(&remapped)
		}
	}
}

// handleUpstreamFailure tears down all clients and schedules reconnection.
func (m *ManagedUpstream) handleUpstreamFailure() {
	m.mu.Lock()
	clients := append([]clientWriter(nil), m.clients...)
	m.mu.Unlock()

	for _, cw := range clients {
		cw.Abort()
	}

	go m.reconnectLoop()
}
```

**Step 4: Run to confirm tests pass**

```bash
go test ./proxy/... -run "TestReadLoop" -v
```

Expected: PASS

**Step 5: Run all proxy tests**

```bash
go test ./proxy/... -short -v 2>&1 | tail -20
```

Expected: all PASS

**Step 6: Commit**

```bash
git add proxy/managed_upstream.go proxy/managed_upstream_test.go
git commit -m "feat: add ManagedUpstream read loop with heartbeat and frame dispatch"
```

---

## Task 4: `ManagedUpstream` — Reconnection Loop

Add `reconnectLoop` with exponential backoff. When the upstream dies, all clients are already torn down (Task 3). This task adds the reconnect-and-restart behaviour.

**Files:**
- Modify: `proxy/managed_upstream.go`
- Modify: `proxy/managed_upstream_test.go`

**Step 1: Write a failing test**

Add to `proxy/managed_upstream_test.go`:

```go
func TestReconnectLoop_RestartsReadLoop(t *testing.T) {
	proxyConn, serverConn := upstreamPipe(t)
	uc := &UpstreamConn{
		Conn:   proxyConn,
		Reader: bufio.NewReader(proxyConn),
		Writer: bufio.NewWriter(proxyConn),
	}

	dialCount := 0
	proxyConn2, serverConn2 := upstreamPipe(t)
	_ = serverConn2

	m := newTestManagedUpstream(65535)
	m.dialFn = func() (*UpstreamConn, error) {
		dialCount++
		return &UpstreamConn{
			Conn:   proxyConn2,
			Reader: bufio.NewReader(proxyConn2),
			Writer: bufio.NewWriter(proxyConn2),
		}, nil
	}
	m.reconnectBase = 10 * time.Millisecond // fast reconnect for tests
	m.Start(uc)

	// Kill the upstream connection
	serverConn.Close()

	// Wait for reconnect
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 1, dialCount, "should have dialled once to reconnect")
}
```

**Step 2: Run to confirm it fails**

```bash
go test ./proxy/... -run TestReconnectLoop -v
```

Expected: FAIL — `dialFn`/`reconnectBase` fields not defined

**Step 3: Implement reconnect loop**

Update `ManagedUpstream` struct to add dial fields:

```go
type ManagedUpstream struct {
	username, password, vhost string
	maxChannels               uint16

	dialFn        func() (*UpstreamConn, error)
	reconnectBase time.Duration // base backoff; defaults to 500ms

	mu            sync.Mutex
	conn          *UpstreamConn
	usedChannels  map[uint16]bool
	channelOwners map[uint16]channelEntry
	clients       []clientWriter

	stopped   atomic.Bool
	heartbeat uint16
}
```

Add `reconnectLoop`:

```go
func (m *ManagedUpstream) reconnectLoop() {
	base := m.reconnectBase
	if base == 0 {
		base = 500 * time.Millisecond
	}
	wait := base
	const maxWait = 30 * time.Second

	for !m.stopped.Load() {
		time.Sleep(wait)

		conn, err := m.dialFn()
		if err != nil {
			wait = min(wait*2, maxWait)
			continue
		}

		m.mu.Lock()
		m.conn = conn
		// Reset channel state — all clients were torn down
		m.usedChannels = make(map[uint16]bool)
		m.channelOwners = make(map[uint16]channelEntry)
		m.clients = make([]clientWriter, 0)
		m.mu.Unlock()

		go m.readLoop()
		return
	}
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
```

Update `newTestManagedUpstream` to set a no-op `dialFn` by default:

```go
func newTestManagedUpstream(maxChannels uint16) *ManagedUpstream {
	return &ManagedUpstream{
		username:      "guest",
		password:      "guest",
		vhost:         "/",
		maxChannels:   maxChannels,
		usedChannels:  make(map[uint16]bool),
		channelOwners: make(map[uint16]channelEntry),
		clients:       make([]clientWriter, 0),
		dialFn:        func() (*UpstreamConn, error) { return nil, errors.New("no dial fn") },
	}
}
```

**Step 4: Run to confirm the test passes**

```bash
go test ./proxy/... -run TestReconnectLoop -v
```

Expected: PASS

**Step 5: Run all proxy tests**

```bash
go test ./proxy/... -short 2>&1
```

Expected: all PASS

**Step 6: Commit**

```bash
git add proxy/managed_upstream.go proxy/managed_upstream_test.go
git commit -m "feat: add ManagedUpstream reconnection with exponential backoff"
```

---

## Task 5: Wire `Proxy` to Use `ManagedUpstream`

Replace the pool map in `Proxy` with a `ManagedUpstream` map. Add `getOrCreateManagedUpstream`. Update `Start`/`Stop`.

**Files:**
- Modify: `proxy/proxy.go`
- Modify: `proxy/proxy_test.go`

**Step 1: Write a failing test**

Add to `proxy/proxy_test.go`:

```go
func TestGetOrCreateManagedUpstream_ReturnsSameInstance(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15680,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 5,
		PoolMaxChannels: 65535,
	}
	p, err := NewProxy(cfg)
	require.NoError(t, err)

	m1, err := p.getOrCreateManagedUpstream("user", "pass", "/")
	require.NoError(t, err)

	m2, err := p.getOrCreateManagedUpstream("user", "pass", "/")
	require.NoError(t, err)

	assert.Same(t, m1, m2, "same credentials should return the same ManagedUpstream")

	m3, err := p.getOrCreateManagedUpstream("other", "pass", "/")
	require.NoError(t, err)
	assert.NotSame(t, m1, m3, "different credentials should return different ManagedUpstream")
}
```

**Step 2: Run to confirm it fails**

```bash
go test ./proxy/... -run TestGetOrCreateManagedUpstream -v
```

Expected: FAIL — method not defined

**Step 3: Update `proxy/proxy.go`**

Replace the `pools` field and related code:

```go
type Proxy struct {
	listener    *AMQPListener
	config      *config.Config
	upstreams   map[[32]byte]*ManagedUpstream
	netListener net.Listener
	mu          sync.RWMutex
	wg          sync.WaitGroup // tracks active handleConnection goroutines
}

func NewProxy(cfg *config.Config) (*Proxy, error) {
	listener, err := NewAMQPListener(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}
	return &Proxy{
		listener:  listener,
		config:    cfg,
		upstreams: make(map[[32]byte]*ManagedUpstream),
	}, nil
}
```

Add `getOrCreateManagedUpstream`:

```go
func (p *Proxy) getOrCreateManagedUpstream(username, password, vhost string) (*ManagedUpstream, error) {
	key := p.getPoolKey(username, password, vhost)

	p.mu.RLock()
	existing, ok := p.upstreams[key]
	p.mu.RUnlock()
	if ok && existing.HasCapacity() {
		return existing, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Re-check under write lock
	if existing, ok := p.upstreams[key]; ok && existing.HasCapacity() {
		return existing, nil
	}

	// Dial new upstream
	network, addr, err := parseUpstreamURL(p.config.UpstreamURL)
	if err != nil {
		return nil, err
	}

	netConn, err := p.dialUpstream(network, addr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial upstream: %w", err)
	}

	upstreamConn, err := performUpstreamHandshake(netConn, username, password, vhost)
	if err != nil {
		netConn.Close()
		return nil, fmt.Errorf("upstream handshake failed: %w", err)
	}

	m := &ManagedUpstream{
		username:    username,
		password:    password,
		vhost:       vhost,
		maxChannels: uint16(p.config.PoolMaxChannels),
		usedChannels:  make(map[uint16]bool),
		channelOwners: make(map[uint16]channelEntry),
		clients:       make([]clientWriter, 0),
	}
	m.dialFn = func() (*UpstreamConn, error) {
		nc, err := p.dialUpstream(network, addr)
		if err != nil {
			return nil, err
		}
		return performUpstreamHandshake(nc, username, password, vhost)
	}
	m.Start(upstreamConn)

	p.upstreams[key] = m
	return m, nil
}

func (p *Proxy) dialUpstream(network, addr string) (net.Conn, error) {
	if network == "tcp+tls" {
		tlsCfg, err := p.upstreamTLSConfig()
		if err != nil {
			return nil, err
		}
		return tls.Dial("tcp", addr, tlsCfg)
	}
	return net.Dial("tcp", addr)
}
```

Update `Stop()` to close all `ManagedUpstream`s and drain:

```go
func (p *Proxy) Stop() error {
	p.mu.Lock()
	if p.netListener != nil {
		p.netListener.Close()
		p.netListener = nil
	}
	for _, m := range p.upstreams {
		m.stopped.Store(true)
		m.mu.Lock()
		if m.conn != nil {
			m.conn.Conn.Close()
		}
		m.mu.Unlock()
	}
	p.upstreams = make(map[[32]byte]*ManagedUpstream)
	p.mu.Unlock()

	// Wait up to 30 seconds for active handleConnection goroutines to exit.
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
	}
	return nil
}
```

Add `"time"` to imports if not already present.

**Step 4: Run to confirm the test passes**

```bash
go test ./proxy/... -run TestGetOrCreateManagedUpstream -v
```

Expected: PASS

**Step 5: Run all proxy tests**

```bash
go test ./proxy/... -short 2>&1
```

Expected: PASS (existing tests may need minor fixes — the `pools` field is gone)

**Step 6: Commit**

```bash
git add proxy/proxy.go proxy/proxy_test.go
git commit -m "feat: wire Proxy to use ManagedUpstream, add WaitGroup drain to Stop()"
```

---

## Task 6: Rewrite `handleConnection` for Multiplexing

Replace the current two-direction loop with: register with `ManagedUpstream`, run client→upstream only (upstream→client is handled by the read loop), detect `Channel.Open`/`Close` for channel allocation.

**Files:**
- Modify: `proxy/proxy.go`
- Modify: `proxy/connection.go` (add `isChannelOpen`, `isChannelClose` helpers)
- Modify: `proxy/connection_test.go`

**Step 1: Write failing tests for channel frame detection**

Add to `proxy/connection_test.go`:

```go
func TestIsChannelOpen(t *testing.T) {
	// Channel.Open: class=20 (0x00,0x14), method=10 (0x00,0x0A)
	frame := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{0, 20, 0, 10}}
	assert.True(t, isChannelOpen(frame))

	// Not Channel.Open
	other := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{0, 20, 0, 11}}
	assert.False(t, isChannelOpen(other))
}

func TestIsChannelClose(t *testing.T) {
	// Channel.Close: class=20, method=40 (0x00,0x28)
	frame := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{0, 20, 0, 40}}
	assert.True(t, isChannelClose(frame))

	// Channel.CloseOk: class=20, method=41 (0x00,0x29)
	frame2 := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{0, 20, 0, 41}}
	assert.True(t, isChannelClose(frame2))
}
```

**Step 2: Run to confirm they fail**

```bash
go test ./proxy/... -run "TestIsChannel" -v
```

Expected: FAIL

**Step 3: Add helpers to `proxy/connection.go`**

```go
// isChannelOpen returns true for Channel.Open frames (class=20, method=10).
func isChannelOpen(frame *Frame) bool {
	if frame.Type != FrameTypeMethod || len(frame.Payload) < 4 {
		return false
	}
	return frame.Payload[0] == 0 && frame.Payload[1] == 20 &&
		frame.Payload[2] == 0 && frame.Payload[3] == 10
}

// isChannelClose returns true for Channel.Close (40) and Channel.CloseOk (41).
func isChannelClose(frame *Frame) bool {
	if frame.Type != FrameTypeMethod || len(frame.Payload) < 4 {
		return false
	}
	if frame.Payload[0] != 0 || frame.Payload[1] != 20 {
		return false
	}
	return frame.Payload[3] == 40 || frame.Payload[3] == 41
}
```

**Step 4: Verify helpers pass**

```bash
go test ./proxy/... -run "TestIsChannel" -v
```

Expected: PASS

**Step 5: Rewrite `handleConnection` in `proxy/proxy.go`**

Replace the current `handleConnection`:

```go
func (p *Proxy) handleConnection(clientConn net.Conn) {
	p.wg.Add(1)
	defer p.wg.Done()
	defer clientConn.Close()

	cc := NewClientConnection(clientConn, p)

	// Read and validate AMQP protocol header.
	header := make([]byte, 8)
	if _, err := io.ReadFull(clientConn, header); err != nil {
		return
	}
	if string(header) != ProtocolHeader {
		return
	}

	// Client-side handshake: extracts credentials and vhost.
	if err := cc.Handle(); err != nil {
		return
	}

	cc.Mu.RLock()
	connPool := cc.Pool
	cc.Mu.RUnlock()
	if connPool == nil {
		return
	}

	// Acquire or create a ManagedUpstream for this credential set.
	managed, err := p.getOrCreateManagedUpstream(connPool.Username, connPool.Password, connPool.Vhost)
	if err != nil {
		// Upstream unavailable — send Connection.Close 503 to client.
		_ = WriteFrame(cc.Writer, &Frame{
			Type:    FrameTypeMethod,
			Channel: 0,
			Payload: serializeConnectionClose(503, "upstream unavailable"),
		})
		_ = cc.Writer.Flush()
		return
	}

	managed.Register(cc)
	defer func() {
		managed.Deregister(cc)
		p.releaseClientChannels(managed, cc)
	}()

	// Client → Upstream proxy loop.
	for {
		frame, err := ParseFrame(cc.Reader)
		if err != nil {
			return
		}

		// Consume client heartbeats — proxy owns the upstream heartbeat.
		if frame.Type == FrameTypeHeartbeat {
			continue
		}

		if isChannelOpen(frame) {
			upstreamID, err := managed.AllocateChannel(frame.Channel, cc)
			if err != nil {
				// No channel capacity — close this client.
				return
			}
			cc.MapChannel(frame.Channel, upstreamID)
		}

		// Remap client channel ID to upstream channel ID.
		cc.Mu.RLock()
		upstreamChanID, mapped := cc.ChannelMapping[frame.Channel]
		cc.Mu.RUnlock()

		remapped := *frame
		if mapped {
			remapped.Channel = upstreamChanID
		}

		// Mark channel unsafe if needed (so we know to close it on disconnect).
		markChannelSafety(frame, cc)

		managed.mu.Lock()
		conn := managed.conn
		managed.mu.Unlock()
		if conn == nil {
			return
		}

		if err := WriteFrame(conn.Writer, &remapped); err != nil {
			return
		}
		if err := conn.Writer.Flush(); err != nil {
			return
		}

		if isChannelClose(frame) {
			cc.Mu.RLock()
			upstreamID := cc.ChannelMapping[frame.Channel]
			cc.Mu.RUnlock()
			managed.ReleaseChannel(upstreamID)
			cc.UnmapChannel(frame.Channel)
		}
	}
}
```

Add `serializeConnectionClose` to `proxy/connection.go`:

```go
// serializeConnectionClose builds a Connection.Close method payload.
func serializeConnectionClose(replyCode uint16, replyText string) []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 50})
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, replyCode)
	body = append(body, serializeShortString(replyText)...)
	body = append(body, 0, 0, 0, 0) // class-id=0, method-id=0
	return append(header, body...)
}
```

Add `releaseClientChannels` to `proxy/proxy.go`:

```go
// releaseClientChannels is called when a client disconnects. Safe channels are
// released for reuse; unsafe channels are closed on the upstream first.
func (p *Proxy) releaseClientChannels(managed *ManagedUpstream, cc *ClientConnection) {
	cc.Mu.RLock()
	channels := make(map[uint16]uint16, len(cc.ChannelMapping))
	for clientID, upstreamID := range cc.ChannelMapping {
		channels[clientID] = upstreamID
	}
	cc.Mu.RUnlock()

	for clientID, upstreamID := range channels {
		cc.Mu.RLock()
		ch, ok := cc.ClientChannels[clientID]
		cc.Mu.RUnlock()

		if ok && !ch.Safe {
			// Send Channel.Close to upstream for unsafe channels.
			managed.mu.Lock()
			conn := managed.conn
			managed.mu.Unlock()
			if conn != nil {
				closePayload := SerializeMethodHeader(&MethodHeader{ClassID: 20, MethodID: 40})
				closePayload = append(closePayload, 0, 0)                         // reply-code = 0
				closePayload = append(closePayload, serializeShortString("")...)   // reply-text = ""
				closePayload = append(closePayload, 0, 0, 0, 0)                   // class-id=0, method-id=0
				_ = WriteFrame(conn.Writer, &Frame{
					Type:    FrameTypeMethod,
					Channel: upstreamID,
					Payload: closePayload,
				})
				_ = conn.Writer.Flush()
			}
		}

		managed.ReleaseChannel(upstreamID)
		cc.UnmapChannel(clientID)
	}
}
```

**Step 6: Add `markChannelSafety` to `proxy/connection.go`**

```go
var unsafeMethods = map[[4]byte]bool{
	{0, 60, 0, 20}:  true, // Basic.Consume
	{0, 60, 0, 10}:  true, // Basic.Qos
	{0, 60, 0, 80}:  true, // Basic.Ack
	{0, 60, 0, 90}:  true, // Basic.Reject
	{0, 60, 0, 120}: true, // Basic.Nack
}

// markChannelSafety marks a channel unsafe if the frame is an unsafe method.
func markChannelSafety(frame *Frame, cc *ClientConnection) {
	if frame.Type != FrameTypeMethod || len(frame.Payload) < 4 {
		return
	}
	key := [4]byte{frame.Payload[0], frame.Payload[1], frame.Payload[2], frame.Payload[3]}
	if unsafeMethods[key] {
		cc.Mu.RLock()
		ch, ok := cc.ClientChannels[frame.Channel]
		cc.Mu.RUnlock()
		if ok {
			ch.Mu.Lock()
			ch.Safe = false
			ch.Mu.Unlock()
		}
	}
}
```

**Step 7: Build and run all tests**

```bash
go build ./... && go test ./... -short 2>&1
```

Fix any compilation errors (likely missing imports). Expected: all PASS.

**Step 8: Commit**

```bash
git add proxy/proxy.go proxy/connection.go proxy/connection_test.go
git commit -m "feat: rewrite handleConnection for multiplexing — register with ManagedUpstream, client→upstream only"
```

---

## Task 7: Signal Handling in `main.go`

Add `SIGTERM`/`SIGINT` handling so `Stop()` is called on graceful shutdown signals.

**Files:**
- Modify: `main.go`

**Step 1: Read the current `main.go`**

```bash
cat main.go
```

**Step 2: Add signal handling**

Replace the `p.Start()` call with:

```go
import (
	"os"
	"os/signal"
	"syscall"
)

// In main(), after proxy is created and before Start():
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

go func() {
	<-sigCh
	p.Stop()
}()

if err := p.Start(); err != nil {
	log.Fatal(err)
}
```

**Step 3: Build**

```bash
go build ./...
```

Expected: no errors

**Step 4: Commit**

```bash
git add main.go
git commit -m "feat: handle SIGTERM/SIGINT for graceful shutdown drain"
```

---

## Task 8: Remove Dead Pool Code

`pool.Connection` interface, `pool.PooledConnection` (with its `Connection` field and `SafeChannels`), and `pool.ConnectionPool` are all replaced by `ManagedUpstream`. `pool.Channel` is also no longer referenced. The `pool` package can be removed entirely, or slimmed to just `Channel` if it's still used elsewhere.

**Step 1: Check what's still used from `pool`**

```bash
grep -r "pool\." --include="*.go" proxy/ tests/ main.go | grep -v "_test.go" | grep -v "pool/pool.go"
```

**Step 2: Remove `Pool` field from `ClientConnection`**

In `proxy/connection.go`, remove:
- `Pool *pool.ConnectionPool` field from `ClientConnection`
- The `import "github.com/timsweb/amqproxy/pool"` if no longer needed

**Step 3: Update `proxy/connection.go` Handle() method**

`Handle()` currently sets `cc.Pool`. Update it to return the credential set instead:

```go
// Handle performs the client-side AMQP handshake and returns the extracted credentials.
// On success, cc.Reader and cc.Writer are ready for frame I/O.
func (cc *ClientConnection) Handle() (*Credentials, string, error) {
    // ... same logic but return creds and vhost instead of setting cc.Pool
}
```

Update `handleConnection` in `proxy.go` to use the returned values.

**Step 4: Remove or slim `pool/pool.go`**

If `pool.Channel` is not referenced anywhere outside `pool/`, delete `pool/` entirely. If it is still useful for operation tracking, keep `channel.go` only and delete `pool.go` and `pool_test.go` tests that relate to `ConnectionPool`/`PooledConnection`.

**Step 5: Build and run all tests**

```bash
go build ./... && go test ./... -short 2>&1
```

Expected: all PASS.

**Step 6: Commit**

```bash
git add -A
git commit -m "refactor: remove dead pool.ConnectionPool and PooledConnection — replaced by ManagedUpstream"
```

---

## Task 9: Full Test Suite Verification

Run everything with the race detector and fix any issues.

**Step 1: Run with race detector**

```bash
go test -short -race ./... 2>&1
```

Expected: PASS, no races.

**Step 2: Run vet**

```bash
go vet ./... 2>&1
```

Expected: no warnings.

**Step 3: Build the binary**

```bash
go build -o /tmp/amqproxy . && echo "build OK"
```

**Step 4: Fix any failures found**

Read the output and fix systematically. Common issues:
- Import cycles introduced by new files
- Missing `sync/atomic` imports
- `min` function name conflicts (Go 1.21+ has a builtin `min` — rename to `minDuration` if needed)

**Step 5: Final commit**

```bash
git add -A
git commit -m "fix: all tests pass with race detector after multiplexing implementation"
```

---

## Verification Checklist

- [ ] `go build ./...` succeeds
- [ ] `go vet ./...` produces no warnings
- [ ] `go test -short -race ./...` passes with no races
- [ ] `TestAllocateChannel_AssignsLowestFreeID` passes
- [ ] `TestAllocateChannel_ErrorWhenFull` passes
- [ ] `TestReadLoop_HeartbeatEchoed` passes
- [ ] `TestReadLoop_FrameDispatchedToClient` passes
- [ ] `TestReadLoop_HeartbeatNotSentToClients` passes
- [ ] `TestReconnectLoop_RestartsReadLoop` passes
- [ ] `TestGetOrCreateManagedUpstream_ReturnsSameInstance` passes
- [ ] `TestIsChannelOpen` passes
- [ ] `TestIsChannelClose` passes
- [ ] `TestClientConnectionDeliverFrame` passes
- [ ] Multiple clients can share one upstream (verified by `TestGetOrCreateManagedUpstream_ReturnsSameInstance`)
- [ ] `Stop()` drains within 30 seconds
- [ ] SIGTERM triggers graceful shutdown
- [ ] Heartbeats echoed to upstream, not forwarded to clients
- [ ] Unsafe channels closed on upstream when client disconnects
- [ ] Safe channels released for reuse when client disconnects
