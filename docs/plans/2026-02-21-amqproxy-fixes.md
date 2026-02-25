# AMQProxy Fix Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix all critical bugs, structural issues, and test failures identified in code review so the proxy actually proxies AMQP traffic.

**Architecture:** The fixes work outward from the core: first correct the malformed AMQP frames the proxy sends, then implement the missing upstream connection logic, then wire up the bidirectional frame loop in `handleConnection`, then fix correctness issues in the pool and channel tracking, and finally fix the broken tests. Each task is independently testable.

**Tech Stack:** Go 1.22+, standard library (`net`, `crypto/tls`, `bufio`, `io`), `github.com/stretchr/testify/assert` for tests.

---

## Background: How AMQP 0-9-1 Handshake Works

The proxy sits between AMQP clients and an upstream RabbitMQ. Two handshakes happen in sequence.

**Client → Proxy handshake (proxy acts as server):**
1. Client sends 8-byte protocol header: `AMQP\x00\x00\x09\x01`
2. Proxy sends `Connection.Start` (class=10, method=10): version, mechanisms, locales
3. Client sends `Connection.StartOk` (class=10, method=11): credentials in PLAIN format
4. Proxy sends `Connection.Tune` (class=10, method=30): channel-max, frame-max, heartbeat
5. Client sends `Connection.TuneOk` (class=10, method=31): agreed parameters
6. Client sends `Connection.Open` (class=10, method=40): vhost
7. Proxy sends `Connection.OpenOK` (class=10, method=41)

**Proxy → Upstream handshake (proxy acts as client):**
1. Proxy sends 8-byte protocol header
2. Upstream sends `Connection.Start`
3. Proxy sends `Connection.StartOk` with the client's credentials
4. Upstream sends `Connection.Tune`
5. Proxy sends `Connection.TuneOk`
6. Proxy sends `Connection.Open` with the client's vhost
7. Upstream sends `Connection.OpenOK`

After both handshakes complete, the proxy runs two goroutines: one copying client→upstream frames (remapping channel IDs), one copying upstream→client frames (remapping back).

**AMQP frame wire format:**
```
[1 byte type][2 bytes channel][4 bytes payload size][N bytes payload][1 byte 0xCE end marker]
```

**Method payload format:**
```
[2 bytes class-id][2 bytes method-id][... method-specific fields ...]
```

**AMQP field types:**
- `octet`: 1 byte
- `short`: 2 bytes big-endian uint16
- `long`: 4 bytes big-endian uint32
- `shortstr`: 1-byte length prefix + N bytes
- `longstr`: 4-byte length prefix + N bytes
- `table`: 4-byte length prefix + N bytes of key-value pairs

---

## Task 1: Fix Malformed `Connection.Start` Frame

**Files:**
- Modify: `proxy/connection.go:196-199`
- Modify: `proxy/connection_test.go`

The current `serializeConnectionStart()` appends 4 zero bytes after the method header. A real AMQP client will reject this because it expects `version-major` (1 byte), `version-minor` (1 byte), `server-properties` (table), `mechanisms` (longstr), `locales` (longstr).

**Step 1: Write a failing test**

Add to `proxy/connection_test.go`:

```go
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
```

Add `"encoding/binary"` to imports in the test file if not already present.

**Step 2: Run to confirm it fails**

```bash
go test ./proxy/... -run TestSerializeConnectionStart -v
```

Expected: FAIL

**Step 3: Fix `serializeConnectionStart` in `proxy/connection.go`**

Replace lines 196-199:

```go
func serializeConnectionStart() []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 10})

	payload := make([]byte, 0, 32)
	payload = append(payload, 0)                      // version-major = 0
	payload = append(payload, 9)                      // version-minor = 9
	payload = append(payload, serializeEmptyTable()...) // server-properties (empty table)
	payload = append(payload, serializeLongString([]byte("PLAIN"))...) // mechanisms
	payload = append(payload, serializeLongString([]byte("en_US"))...) // locales

	return append(header, payload...)
}
```

Note: `serializeEmptyTable()` and `serializeLongString()` already exist in `proxy/credentials.go`.

**Step 4: Run to confirm it passes**

```bash
go test ./proxy/... -run TestSerializeConnectionStart -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add proxy/connection.go proxy/connection_test.go
git commit -m "fix: correct malformed Connection.Start frame serialization"
```

---

## Task 2: Fix Malformed `Connection.Tune` Frame

**Files:**
- Modify: `proxy/connection.go:177-194`
- Modify: `proxy/connection_test.go`

The current `sendConnectionTune()` sets `payload[6] = 60` treating heartbeat as 1 byte. AMQP spec defines heartbeat as a `short` (uint16 = 2 bytes). The method has 8 bytes total (channel-max: 2, frame-max: 4, heartbeat: 2) but the code allocates 9 bytes.

**Step 1: Write a failing test**

Add to `proxy/connection_test.go`:

```go
func TestSendConnectionTunePayload(t *testing.T) {
	// serializeConnectionTunePayload should produce the correct 8-byte body
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
```

**Step 2: Run to confirm it fails**

```bash
go test ./proxy/... -run TestSendConnectionTunePayload -v
```

Expected: FAIL — `serializeConnectionTunePayload` not defined

**Step 3: Refactor `sendConnectionTune` in `proxy/connection.go`**

Replace lines 177-194:

```go
func serializeConnectionTunePayload() []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 30})
	body := make([]byte, 8) // channel-max(2) + frame-max(4) + heartbeat(2)
	binary.BigEndian.PutUint16(body[0:2], 0)      // channel-max = 0 (no limit)
	binary.BigEndian.PutUint32(body[2:6], 131072) // frame-max = 128KB
	binary.BigEndian.PutUint16(body[6:8], 60)     // heartbeat = 60s
	return append(header, body...)
}

func (cc *ClientConnection) sendConnectionTune() error {
	if err := WriteFrame(cc.Writer, &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: serializeConnectionTunePayload(),
	}); err != nil {
		return err
	}
	return cc.Writer.Flush()
}
```

**Step 4: Run to confirm it passes**

```bash
go test ./proxy/... -run TestSendConnectionTunePayload -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add proxy/connection.go proxy/connection_test.go
git commit -m "fix: correct malformed Connection.Tune frame (heartbeat must be uint16)"
```

---

## Task 3: Fix `Handle()` — Vhost Parsing and Error Handling

**Files:**
- Modify: `proxy/connection.go:147-162`
- Modify: `proxy/credentials.go`
- Modify: `proxy/credentials_test.go`

Two bugs: (1) vhost is hardcoded to `"/"` instead of parsed from `Connection.Open`; (2) if `Connection.Open` is not received, `Handle()` sends `OpenOK` anyway and returns `nil`.

**Step 1: Add `ParseConnectionOpen` to `proxy/credentials.go`**

The `Connection.Open` frame payload (after method header at offset 4):
- vhost: shortstr (1-byte length + content)
- reserved1: shortstr (ignore)
- reserved2: bits (ignore)

```go
// ParseConnectionOpen extracts the vhost from a Connection.Open frame payload.
func ParseConnectionOpen(data []byte) (string, error) {
	header, err := ParseMethodHeader(data)
	if err != nil {
		return "", err
	}
	if header.ClassID != 10 || header.MethodID != 40 {
		return "", fmt.Errorf("expected Connection.Open (class=10, method=40), got class=%d method=%d", header.ClassID, header.MethodID)
	}
	if len(data) < 5 {
		return "", errors.New("Connection.Open payload too short")
	}
	vhost, _, err := parseShortString(data[4:])
	if err != nil {
		return "", fmt.Errorf("failed to parse vhost: %w", err)
	}
	return vhost, nil
}
```

Add `"fmt"` to imports in `proxy/credentials.go` if not already present.

**Step 2: Write a failing test in `proxy/credentials_test.go`**

```go
func TestParseConnectionOpen(t *testing.T) {
	// Build a Connection.Open payload: method header + shortstr vhost
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 40})
	vhostBytes := serializeShortString("/staging")
	payload := append(header, vhostBytes...)

	vhost, err := ParseConnectionOpen(payload)
	assert.NoError(t, err)
	assert.Equal(t, "/staging", vhost)
}

func TestParseConnectionOpenDefaultVhost(t *testing.T) {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 40})
	vhostBytes := serializeShortString("/")
	payload := append(header, vhostBytes...)

	vhost, err := ParseConnectionOpen(payload)
	assert.NoError(t, err)
	assert.Equal(t, "/", vhost)
}

func TestParseConnectionOpenWrongMethod(t *testing.T) {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 11})
	payload := append(header, 0)

	_, err := ParseConnectionOpen(payload)
	assert.Error(t, err)
}
```

**Step 3: Run to confirm it fails**

```bash
go test ./proxy/... -run TestParseConnectionOpen -v
```

Expected: FAIL — `ParseConnectionOpen` not defined

**Step 4: Add the function**

Add the `ParseConnectionOpen` function from Step 1 to `proxy/credentials.go`.

**Step 5: Run to confirm tests pass**

```bash
go test ./proxy/... -run TestParseConnectionOpen -v
```

Expected: PASS

**Step 6: Fix `Handle()` in `proxy/connection.go`**

Replace lines 147-162:

```go
	if header.ClassID != 10 || header.MethodID != 40 {
		return fmt.Errorf("expected Connection.Open (class=10, method=40), got class=%d method=%d", header.ClassID, header.MethodID)
	}

	vhost, err := ParseConnectionOpen(frame.Payload)
	if err != nil {
		return fmt.Errorf("failed to parse Connection.Open: %w", err)
	}

	connPool := cc.Proxy.getOrCreatePool(creds.Username, creds.Password, vhost)
	cc.Mu.Lock()
	cc.Pool = connPool
	cc.Mu.Unlock()

	if err := WriteFrame(cc.Writer, &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: serializeConnectionOpenOK(),
	}); err != nil {
		return err
	}
	return cc.Writer.Flush()
```

**Step 7: Run all proxy tests**

```bash
go test ./proxy/... -v
```

Expected: PASS

**Step 8: Commit**

```bash
git add proxy/connection.go proxy/credentials.go proxy/credentials_test.go
git commit -m "fix: parse vhost from Connection.Open; return error if not received"
```

---

## Task 4: Fix `UnmapChannel` — Clean Up `ClientChannels`

**Files:**
- Modify: `proxy/connection.go:64-72`
- Modify: `proxy/connection_test.go`

`UnmapChannel` removes from `ChannelMapping` and `ReverseMapping` but leaves a stale entry in `ClientChannels`. Subsequent `RecordChannelOperation` calls on the unmapped channel would succeed against a zombie object.

**Step 1: Write a failing test**

Add to `proxy/connection_test.go`:

```go
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
```

**Step 2: Run to confirm it fails**

```bash
go test ./proxy/... -run TestUnmapChannelCleansClientChannels -v
```

Expected: FAIL

**Step 3: Fix `UnmapChannel` in `proxy/connection.go`**

Replace lines 64-72:

```go
func (cc *ClientConnection) UnmapChannel(clientID uint16) {
	cc.Mu.Lock()
	defer cc.Mu.Unlock()
	upstreamID, ok := cc.ChannelMapping[clientID]
	if ok {
		delete(cc.ChannelMapping, clientID)
		delete(cc.ReverseMapping, upstreamID)
		delete(cc.ClientChannels, clientID)
	}
}
```

**Step 4: Run to confirm it passes**

```bash
go test ./proxy/... -run TestUnmapChannelCleansClientChannels -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add proxy/connection.go proxy/connection_test.go
git commit -m "fix: UnmapChannel now also removes from ClientChannels"
```

---

## Task 5: Fix `SafeChannels` — Move to `PooledConnection`

**Files:**
- Modify: `pool/pool.go`
- Modify: `pool/pool_test.go`

`SafeChannels` is on `ConnectionPool` but channel IDs are scoped to individual upstream connections. If a pool has two upstream connections, channel 5 on connection A and channel 5 on connection B are different channels, but `IsSafeChannel(5)` would return `true` for both. Move `SafeChannels` to `PooledConnection`.

**Step 1: Write a failing test**

Add to `pool/pool_test.go`:

```go
func TestSafeChannelsPerConnection(t *testing.T) {
	p := NewConnectionPool("user", "pass", "/", 5, 65535)

	conn1 := &mockConnection{}
	conn2 := &mockConnection{}
	p.AddConnection(conn1)
	p.AddConnection(conn2)

	pc1 := p.GetConnectionByIndex(0)
	pc2 := p.GetConnectionByIndex(1)

	// Mark channel 5 as safe on connection 1 only
	pc1.AddSafeChannel(5)

	assert.True(t, pc1.IsSafeChannel(5))
	assert.False(t, pc2.IsSafeChannel(5)) // Must NOT bleed across connections
}
```

**Step 2: Run to confirm it fails**

```bash
go test ./pool/... -run TestSafeChannelsPerConnection -v
```

Expected: FAIL — `GetConnectionByIndex` and per-connection safe channel methods not defined

**Step 3: Update `pool/pool.go`**

Remove `SafeChannels` from `ConnectionPool`. Add it to `PooledConnection`. Remove the dead `ChannelMappings` field from `PooledConnection` at the same time. Add per-connection safe channel methods and `GetConnectionByIndex`:

```go
package pool

import (
	"sync"
	"time"
)

type Credentials struct {
	Username string
	Password string
	Vhost    string
}

type Connection interface {
	IsOpen() bool
	Close() error
	Channel() (Channel, error)
}

type PooledConnection struct {
	Connection   Connection
	SafeChannels map[uint16]bool
	mu           sync.RWMutex
}

func newPooledConnection(conn Connection) *PooledConnection {
	return &PooledConnection{
		Connection:   conn,
		SafeChannels: make(map[uint16]bool),
	}
}

func (pc *PooledConnection) AddSafeChannel(upstreamID uint16) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.SafeChannels[upstreamID] = true
}

func (pc *PooledConnection) IsSafeChannel(upstreamID uint16) bool {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.SafeChannels[upstreamID]
}

func (pc *PooledConnection) RemoveSafeChannel(upstreamID uint16) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	delete(pc.SafeChannels, upstreamID)
}

type ConnectionPool struct {
	Username    string
	Password    string
	Vhost       string
	IdleTimeout time.Duration
	MaxChannels int
	Connections []*PooledConnection
	LastUsed    time.Time
	mu          sync.RWMutex
}

func NewConnectionPool(username, password, vhost string, idleTimeout int, maxChannels int) *ConnectionPool {
	return &ConnectionPool{
		Username:    username,
		Password:    password,
		Vhost:       vhost,
		IdleTimeout: time.Duration(idleTimeout) * time.Second,
		MaxChannels: maxChannels,
		Connections: make([]*PooledConnection, 0),
		LastUsed:    time.Now(),
	}
}

func (p *ConnectionPool) AddConnection(conn Connection) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Connections = append(p.Connections, newPooledConnection(conn))
}

func (p *ConnectionPool) GetConnection() *PooledConnection {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.Connections) == 0 {
		return nil
	}
	return p.Connections[0]
}

func (p *ConnectionPool) GetConnectionByIndex(i int) *PooledConnection {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if i < 0 || i >= len(p.Connections) {
		return nil
	}
	return p.Connections[i]
}

func (p *ConnectionPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.Connections {
		if conn.Connection != nil {
			conn.Connection.Close()
		}
	}
	p.Connections = nil
	return nil
}
```

**Step 4: Fix any test breakage in `pool/pool_test.go`**

The existing tests that called `p.AddSafeChannel`, `p.IsSafeChannel`, `p.RemoveSafeChannel` now need to go through a `PooledConnection`. Update them to use `p.GetConnectionByIndex(0).AddSafeChannel(...)` etc. If the tests used `p.AddConnection(mockConn)` first, keep that. Remove any test that tested pool-level safe channel methods.

**Step 5: Run all pool tests**

```bash
go test ./pool/... -v
```

Expected: PASS

**Step 6: Commit**

```bash
git add pool/pool.go pool/pool_test.go
git commit -m "fix: move SafeChannels to PooledConnection; remove dead ChannelMappings field"
```

---

## Task 6: Fix Duplicate Protocol Header Constant

**Files:**
- Modify: `proxy/frame.go`
- Modify: `proxy/proxy.go`

`ProtocolHeader` is defined in `proxy/frame.go` and `AMQPProtocolHeader` is defined in `proxy/proxy.go` with identical values. Remove `AMQPProtocolHeader` from `proxy.go` and use `ProtocolHeader` everywhere.

**Step 1: Remove `AMQPProtocolHeader` from `proxy/proxy.go`**

Delete lines 14-16:

```go
const (
	AMQPProtocolHeader = "AMQP\x00\x00\x09\x01"
)
```

**Step 2: Update `parseAMQPHandshake` in `proxy/proxy.go`**

Change line 120:

```go
if string(header) != ProtocolHeader {
```

**Step 3: Build to confirm no errors**

```bash
go build ./...
```

**Step 4: Commit**

```bash
git add proxy/proxy.go
git commit -m "refactor: remove duplicate AMQPProtocolHeader constant, use ProtocolHeader"
```

---

## Task 7: Fix mTLS — Set `ClientAuth` in `listener.go`

**Files:**
- Modify: `proxy/listener.go:42-46`
- Modify: `proxy/listener.go` (add test)

Setting `ClientCAs` without `ClientAuth` has no effect in Go's TLS stack. Clients are never asked to present certificates.

**Step 1: Fix `listener.go`**

After line 45 (`tlsConfig.ClientCAs = caCertPool`), add:

```go
tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
```

**Step 2: Build to confirm no errors**

```bash
go build ./...
```

**Step 3: Commit**

```bash
git add proxy/listener.go
git commit -m "fix: set ClientAuth=RequireAndVerifyClientCert when CA cert is configured for mTLS"
```

---

## Task 8: Implement Upstream AMQP Connection

**Files:**
- Create: `proxy/upstream.go`
- Create: `proxy/upstream_test.go`

This is the most important missing piece. The proxy needs to establish an AMQP connection to the upstream RabbitMQ using the credentials extracted from the client handshake.

**Step 1: Write a failing test**

Create `proxy/upstream_test.go`:

```go
package proxy

import (
	"bufio"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fakeUpstream simulates a minimal RabbitMQ server for testing the upstream handshake.
func fakeUpstream(t *testing.T) (net.Conn, func()) {
	t.Helper()
	serverConn, clientConn, err := createPipe()
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		defer serverConn.Close()
		r := bufio.NewReader(serverConn)
		w := bufio.NewWriter(serverConn)

		// Read protocol header
		header := make([]byte, 8)
		r.Read(header)

		// Send Connection.Start
		start := serializeConnectionStart()
		WriteFrame(w, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: start})
		w.Flush()

		// Read Connection.StartOk
		ParseFrame(r)

		// Send Connection.Tune
		WriteFrame(w, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: serializeConnectionTunePayload()})
		w.Flush()

		// Read Connection.TuneOk
		ParseFrame(r)

		// Read Connection.Open
		ParseFrame(r)

		// Send Connection.OpenOK
		WriteFrame(w, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: serializeConnectionOpenOK()})
		w.Flush()
	}()

	return clientConn, func() { clientConn.Close() }
}

func createPipe() (net.Conn, net.Conn, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	defer ln.Close()

	var serverConn net.Conn
	done := make(chan error, 1)
	go func() {
		var err error
		serverConn, err = ln.Accept()
		done <- err
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		return nil, nil, err
	}
	if err := <-done; err != nil {
		return nil, nil, err
	}
	return serverConn, clientConn, nil
}

func TestDialUpstream(t *testing.T) {
	serverConn, cleanup := fakeUpstream(t)
	defer cleanup()

	upstreamConn, err := performUpstreamHandshake(serverConn, "testuser", "testpass", "/testvhost")
	assert.NoError(t, err)
	assert.NotNil(t, upstreamConn)
}
```

**Step 2: Run to confirm it fails**

```bash
go test ./proxy/... -run TestDialUpstream -v
```

Expected: FAIL — `performUpstreamHandshake` not defined

**Step 3: Create `proxy/upstream.go`**

```go
package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
)

// UpstreamConn represents an established AMQP connection to upstream RabbitMQ.
type UpstreamConn struct {
	Conn   net.Conn
	Reader *bufio.Reader
	Writer *bufio.Writer
}

// performUpstreamHandshake performs the AMQP client handshake against an already-dialed
// net.Conn, using the given credentials and vhost. Returns a ready-to-use UpstreamConn.
func performUpstreamHandshake(conn net.Conn, username, password, vhost string) (*UpstreamConn, error) {
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)

	// Send protocol header
	if _, err := io.WriteString(w, ProtocolHeader); err != nil {
		return nil, fmt.Errorf("failed to send protocol header: %w", err)
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}

	// Receive Connection.Start
	frame, err := ParseFrame(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read Connection.Start: %w", err)
	}
	header, err := ParseMethodHeader(frame.Payload)
	if err != nil {
		return nil, err
	}
	if header.ClassID != 10 || header.MethodID != 10 {
		return nil, fmt.Errorf("expected Connection.Start (10,10), got (%d,%d)", header.ClassID, header.MethodID)
	}

	// Send Connection.StartOk with PLAIN credentials
	response := serializeConnectionStartOkResponse(username, password)
	startOk := serializeConnectionStartOk("PLAIN", response)
	if err := WriteFrame(w, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: startOk}); err != nil {
		return nil, fmt.Errorf("failed to send Connection.StartOk: %w", err)
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}

	// Receive Connection.Tune
	frame, err = ParseFrame(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read Connection.Tune: %w", err)
	}
	header, err = ParseMethodHeader(frame.Payload)
	if err != nil {
		return nil, err
	}
	if header.ClassID != 10 || header.MethodID != 30 {
		return nil, fmt.Errorf("expected Connection.Tune (10,30), got (%d,%d)", header.ClassID, header.MethodID)
	}

	// Send Connection.TuneOk — echo back the same parameters
	if err := WriteFrame(w, &Frame{
		Type:    FrameTypeMethod,
		Channel: 0,
		Payload: serializeConnectionTuneOk(frame.Payload),
	}); err != nil {
		return nil, fmt.Errorf("failed to send Connection.TuneOk: %w", err)
	}

	// Send Connection.Open with vhost
	openPayload := serializeConnectionOpen(vhost)
	if err := WriteFrame(w, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: openPayload}); err != nil {
		return nil, fmt.Errorf("failed to send Connection.Open: %w", err)
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}

	// Receive Connection.OpenOK
	frame, err = ParseFrame(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read Connection.OpenOK: %w", err)
	}
	header, err = ParseMethodHeader(frame.Payload)
	if err != nil {
		return nil, err
	}
	if header.ClassID != 10 || header.MethodID != 41 {
		return nil, fmt.Errorf("expected Connection.OpenOK (10,41), got (%d,%d)", header.ClassID, header.MethodID)
	}

	return &UpstreamConn{Conn: conn, Reader: r, Writer: w}, nil
}

// serializeConnectionTuneOk builds a Connection.TuneOk payload that echoes
// the parameters from the upstream's Connection.Tune frame.
func serializeConnectionTuneOk(tunePayload []byte) []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 31})
	// Tune body is the same wire format as TuneOk: channel-max, frame-max, heartbeat
	// Just echo the 8 bytes of body from the upstream tune.
	if len(tunePayload) >= 12 {
		return append(header, tunePayload[4:12]...)
	}
	// Fallback: send reasonable defaults
	body := serializeConnectionTunePayload()
	return append(header, body[4:]...)
}

// serializeConnectionOpen builds a Connection.Open payload for the given vhost.
func serializeConnectionOpen(vhost string) []byte {
	header := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 40})
	payload := serializeShortString(vhost)
	payload = append(payload, serializeShortString("")...) // reserved1
	payload = append(payload, 0)                           // reserved2 (bit)
	return append(header, payload...)
}
```

**Step 4: Run to confirm the test passes**

```bash
go test ./proxy/... -run TestDialUpstream -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add proxy/upstream.go proxy/upstream_test.go
git commit -m "feat: implement upstream AMQP handshake (performUpstreamHandshake)"
```

---

## Task 9: Fix `FrameProxy` — Connect to Real Network Writers

**Files:**
- Modify: `proxy/frame_proxy.go`
- Modify: `proxy/frame_proxy_test.go`

`FrameProxy.Writer` is a `bytes.Buffer`. Both proxy directions write to it, but nothing reads from it and sends to the network. Replace with two separate `io.Writer` fields: one for upstream, one for client.

**Step 1: Update `proxy/frame_proxy_test.go` to test actual output**

Replace the existing tests with versions that check the bytes written. The channel remapping must be visible in the output:

```go
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
```

**Step 2: Run to confirm tests fail**

```bash
go test ./proxy/... -run "TestProxyClientToUpstream|TestProxyUpstreamToClient" -v
```

Expected: FAIL — signature mismatch

**Step 3: Replace `proxy/frame_proxy.go`**

```go
package proxy

import (
	"io"
	"sync"
)

// FrameProxy remaps AMQP channel IDs and forwards frames between a client
// connection and an upstream connection.
type FrameProxy struct {
	ClientConn     *ClientConnection
	UpstreamWriter io.Writer // writes go to the upstream RabbitMQ connection
	ClientWriter   io.Writer // writes go back to the AMQP client
	mu             sync.Mutex
}

// NewFrameProxy creates a FrameProxy. upstreamWriter receives client→upstream frames;
// clientWriter receives upstream→client frames. Either may be nil in tests.
func NewFrameProxy(clientConn *ClientConnection, upstreamWriter, clientWriter io.Writer) *FrameProxy {
	return &FrameProxy{
		ClientConn:     clientConn,
		UpstreamWriter: upstreamWriter,
		ClientWriter:   clientWriter,
	}
}

// ProxyClientToUpstream remaps the frame's channel from client-space to upstream-space
// and writes it to the upstream writer.
func (fp *FrameProxy) ProxyClientToUpstream(frame *Frame) error {
	fp.ClientConn.Mu.RLock()
	upstreamID, ok := fp.ClientConn.ChannelMapping[frame.Channel]
	fp.ClientConn.Mu.RUnlock()

	remapped := *frame
	if ok {
		remapped.Channel = upstreamID
	}

	fp.mu.Lock()
	defer fp.mu.Unlock()
	return WriteFrame(fp.UpstreamWriter, &remapped)
}

// ProxyUpstreamToClient remaps the frame's channel from upstream-space to client-space
// and writes it to the client writer. Frames with no mapping are silently dropped
// (they belong to a different client).
func (fp *FrameProxy) ProxyUpstreamToClient(frame *Frame) error {
	fp.ClientConn.Mu.RLock()
	clientID, found := fp.ClientConn.ReverseMapping[frame.Channel]
	fp.ClientConn.Mu.RUnlock()

	if !found {
		return nil
	}

	remapped := *frame
	remapped.Channel = clientID

	fp.mu.Lock()
	defer fp.mu.Unlock()
	return WriteFrame(fp.ClientWriter, &remapped)
}
```

**Step 4: Run updated tests**

```bash
go test ./proxy/... -run "TestProxyClientToUpstream|TestProxyUpstreamToClient" -v
```

Expected: PASS

**Step 5: Run all proxy tests to check nothing broke**

```bash
go test ./proxy/... -v
```

Expected: PASS

**Step 6: Commit**

```bash
git add proxy/frame_proxy.go proxy/frame_proxy_test.go
git commit -m "fix: FrameProxy now writes to real io.Writers, not bytes.Buffer; verify remapping in tests"
```

---

## Task 10: Wire Up `handleConnection` — Full Proxy Loop

**Files:**
- Modify: `proxy/proxy.go:97-111`
- Modify: `proxy/proxy_test.go`

This is the core missing piece. `handleConnection` must call `ClientConnection.Handle()`, establish an upstream connection, then run bidirectional frame copying.

**Step 1: Add `parseUpstreamURL` helper to `proxy/proxy.go`**

Add before `handleConnection`:

```go
import (
	"net"
	"net/url"
	// ... existing imports ...
)

func parseUpstreamURL(rawURL string) (network, addr string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid upstream URL: %w", err)
	}
	host := u.Hostname()
	port := u.Port()
	switch u.Scheme {
	case "amqp":
		if port == "" {
			port = "5672"
		}
		return "tcp", net.JoinHostPort(host, port), nil
	case "amqps":
		if port == "" {
			port = "5671"
		}
		return "tcp+tls", net.JoinHostPort(host, port), nil
	default:
		return "", "", fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
}
```

**Step 2: Replace `handleConnection` in `proxy/proxy.go`**

```go
func (p *Proxy) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	cc := NewClientConnection(clientConn, p)

	// Step 1: Read the AMQP protocol header from the client.
	header := make([]byte, 8)
	if _, err := io.ReadFull(clientConn, header); err != nil {
		return
	}
	if string(header) != ProtocolHeader {
		return
	}

	// Step 2: Perform the client-side AMQP handshake. This sends Connection.Start,
	// reads credentials, sends Tune, reads TuneOk, reads Connection.Open (vhost).
	if err := cc.Handle(); err != nil {
		return
	}

	// cc.Pool is now set with the correct credentials and vhost.
	cc.Mu.RLock()
	connPool := cc.Pool
	cc.Mu.RUnlock()
	if connPool == nil {
		return
	}

	// Step 3: Establish upstream connection.
	network, addr, err := parseUpstreamURL(p.config.UpstreamURL)
	if err != nil {
		return
	}

	var upstreamNetConn net.Conn
	if network == "tcp+tls" {
		upstreamNetConn, err = tls.Dial("tcp", addr, p.upstreamTLSConfig())
	} else {
		upstreamNetConn, err = net.Dial("tcp", addr)
	}
	if err != nil {
		return
	}
	defer upstreamNetConn.Close()

	upstreamConn, err := performUpstreamHandshake(upstreamNetConn, connPool.Username, connPool.Password, connPool.Vhost)
	if err != nil {
		return
	}

	// Step 4: Bidirectional frame proxy.
	fp := NewFrameProxy(cc, upstreamConn.Writer, cc.Writer)
	done := make(chan struct{}, 2)

	// Client → Upstream
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			frame, err := ParseFrame(cc.Reader)
			if err != nil {
				return
			}
			if err := fp.ProxyClientToUpstream(frame); err != nil {
				return
			}
			if err := upstreamConn.Writer.Flush(); err != nil {
				return
			}
		}
	}()

	// Upstream → Client
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			frame, err := ParseFrame(upstreamConn.Reader)
			if err != nil {
				return
			}
			if err := fp.ProxyUpstreamToClient(frame); err != nil {
				return
			}
			if err := cc.Writer.Flush(); err != nil {
				return
			}
		}
	}()

	// Wait for either direction to finish, then close both connections.
	<-done
}
```

**Step 3: Add `upstreamTLSConfig` helper to `proxy/proxy.go`**

```go
func (p *Proxy) upstreamTLSConfig() *tls.Config {
	cfg, _ := tlsutil.LoadTLSConfig(
		p.config.TLSCACert,
		p.config.TLSClientCert,
		p.config.TLSClientKey,
		p.config.TLSSkipVerify,
	)
	return cfg
}
```

Add to imports in `proxy/proxy.go`:
```go
"crypto/tls"
"github.com/timsweb/amqplex/tlsutil"
```

**Step 4: Build**

```bash
go build ./...
```

Fix any compile errors (missing imports etc.).

**Step 5: Commit**

```bash
git add proxy/proxy.go
git commit -m "feat: wire up handleConnection — full AMQP proxy loop with upstream connection"
```

---

## Task 11: Fix Proxy Shutdown — `Stop()` Must Close the Listener

**Files:**
- Modify: `proxy/proxy.go`
- Modify: `proxy/proxy_test.go`

`Stop()` closes pooled connections but the `Accept()` loop never exits because it swallows all errors with `continue`. The listener reference also isn't stored anywhere.

**Step 1: Store listener in `Proxy` and fix the accept loop**

Update the `Proxy` struct in `proxy/proxy.go`:

```go
type Proxy struct {
	listener    *AMQPListener
	config      *config.Config
	pools       map[[32]byte]*pool.ConnectionPool
	netListener net.Listener // stored so Stop() can close it
	mu          sync.RWMutex
}
```

Update `Start()`:

```go
func (p *Proxy) Start() error {
	var listener net.Listener
	var err error

	if p.config.TLSCert != "" && p.config.TLSKey != "" {
		listener, err = p.listener.StartTLS()
	} else {
		listener, err = p.listener.StartPlain()
	}
	if err != nil {
		return fmt.Errorf("failed to start listener: %w", err)
	}

	p.mu.Lock()
	p.netListener = listener
	p.mu.Unlock()

	for {
		conn, err := listener.Accept()
		if err != nil {
			// Check if we're shutting down
			select {
			default:
			}
			// net.ErrClosed is returned when the listener is closed by Stop()
			if isClosedError(err) {
				return nil
			}
			// Transient error: log and continue
			continue
		}
		go p.handleConnection(conn)
	}
}

func isClosedError(err error) bool {
	// net package doesn't export a typed error for this
	return err != nil && (err.Error() == "use of closed network connection" ||
		strings.Contains(err.Error(), "use of closed network connection"))
}
```

Add `"strings"` to imports.

Update `Stop()`:

```go
func (p *Proxy) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.netListener != nil {
		p.netListener.Close()
		p.netListener = nil
	}

	for _, pool := range p.pools {
		pool.Close()
	}
	p.pools = make(map[[32]byte]*pool.ConnectionPool)
	return nil
}
```

**Step 2: Build**

```bash
go build ./...
```

**Step 3: Commit**

```bash
git add proxy/proxy.go
git commit -m "fix: Stop() now closes the listener; accept loop exits cleanly on shutdown"
```

---

## Task 12: Fix `TestTLSConnection` — Guard `conn.Write` and use `t.TempDir()`

**Files:**
- Modify: `tests/integration_tls_test.go`
- Modify: `tests/certs.go`

Two bugs: (1) `conn.Write` is called at line 81 even when `conn` is nil (assert at line 75 doesn't abort the test). (2) Cert files are written to a relative path instead of `t.TempDir()`.

**Step 1: Fix `tests/integration_tls_test.go`**

Replace the whole file:

```go
package tests

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timsweb/amqplex/config"
	"github.com/timsweb/amqplex/proxy"
)

func TestTLSConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	certDir := t.TempDir()

	caCert, caKey := GenerateTestCA()
	serverCert := GenerateServerCert(caCert, caKey)
	clientCert := GenerateClientCert(caCert, caKey)

	err := WriteCerts(certDir, caCert, caKey, serverCert, clientCert)
	require.NoError(t, err)

	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15674,
		PoolIdleTimeout: 5,
		PoolMaxChannels: 65535,
		TLSCert:         filepath.Join(certDir, "server.crt"),
		TLSKey:          filepath.Join(certDir, "server.key"),
		// No TLSCACert — not requiring client certs for this test
	}

	p, err := proxy.NewProxy(cfg)
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()

	caCertPool := x509.NewCertPool()
	caData, err := os.ReadFile(filepath.Join(certDir, "ca.crt"))
	require.NoError(t, err)
	caCertPool.AppendCertsFromPEM(caData)

	tlsDialCfg := &tls.Config{RootCAs: caCertPool}

	var conn net.Conn
	for i := 0; i < 10; i++ {
		conn, err = tls.Dial("tcp", "localhost:15674", tlsDialCfg)
		if err == nil {
			break
		}
		time.Sleep(time.Duration(i+1) * 50 * time.Millisecond)
	}
	require.NoError(t, err, "failed to connect to TLS proxy after retries")
	require.NotNil(t, conn)
	defer conn.Close()

	// Send AMQP protocol header — proxy should respond with Connection.Start
	_, err = conn.Write([]byte(ProtocolHeaderBytes))
	require.NoError(t, err)

	// Read response frame — expect Connection.Start (class=10, method=10)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	require.Greater(t, n, 7, "expected a full frame")
	// Frame type=1 (method), channel=0
	assert.Equal(t, byte(1), buf[0])
	// After 7-byte frame header: method class=10, method=10
	assert.Equal(t, byte(0), buf[7])
	assert.Equal(t, byte(10), buf[8])
	assert.Equal(t, byte(0), buf[9])
	assert.Equal(t, byte(10), buf[10])
}

func TestConnectionMultiplexing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	certDir := t.TempDir()
	caCert, caKey := GenerateTestCA()
	serverCert := GenerateServerCert(caCert, caKey)
	clientCert := GenerateClientCert(caCert, caKey)

	err := WriteCerts(certDir, caCert, caKey, serverCert, clientCert)
	require.NoError(t, err)

	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15675,
		PoolIdleTimeout: 5,
		PoolMaxChannels: 65535,
		TLSCert:         filepath.Join(certDir, "server.crt"),
		TLSKey:          filepath.Join(certDir, "server.key"),
	}

	p, _ := proxy.NewProxy(cfg)
	go p.Start()
	defer p.Stop()

	caCertPool := x509.NewCertPool()
	caData, _ := os.ReadFile(filepath.Join(certDir, "ca.crt"))
	caCertPool.AppendCertsFromPEM(caData)

	tlsDialCfg := &tls.Config{RootCAs: caCertPool}

	// Connect 3 clients, each sends the protocol header, each must receive Connection.Start
	for i := 0; i < 3; i++ {
		var conn net.Conn
		for j := 0; j < 10; j++ {
			conn, err = tls.Dial("tcp", "localhost:15675", tlsDialCfg)
			if err == nil {
				break
			}
			time.Sleep(time.Duration(j+1) * 50 * time.Millisecond)
		}
		require.NoError(t, err, "client %d failed to connect", i)
		defer conn.Close()

		_, err = conn.Write([]byte(ProtocolHeaderBytes))
		require.NoError(t, err)

		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 512)
		n, err := conn.Read(buf)
		require.NoError(t, err)
		require.Greater(t, n, 7, "client %d got no response", i)
		assert.Equal(t, byte(1), buf[0], "client %d: expected method frame", i)
	}
}
```

Export the protocol header constant for use in tests — add to `proxy/frame.go` or expose via a package-level var. Simplest: add a package-level constant accessible from the tests package. Since tests are in package `tests` (not `proxy`), create a small exported constant in proxy:

Add to `proxy/frame.go`:
```go
// ProtocolHeaderBytes is the AMQP 0-9-1 protocol header as a byte string.
const ProtocolHeaderBytes = ProtocolHeader
```

Or just inline the literal in the test file: `[]byte("AMQP\x00\x00\x09\x01")`.

**Step 2: Update `WriteCerts` in `tests/certs.go` to accept a directory parameter**

Change signature and all `os.WriteFile` calls to use the `dir` parameter:

```go
func WriteCerts(dir string, caCert *x509.Certificate, caKey *rsa.PrivateKey, serverCert *tls.Certificate, clientCert *tls.Certificate) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
	caKeyBytes, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return err
	}
	// PKCS#8 DER must use "PRIVATE KEY" PEM type, not "RSA PRIVATE KEY"
	caKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: caKeyBytes})

	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), caPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.key"), caKeyPEM, 0600); err != nil {
		return err
	}

	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCert.Certificate[0]})
	serverKeyBytes, err := x509.MarshalPKCS8PrivateKey(serverCert.PrivateKey)
	if err != nil {
		return err
	}
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverKeyBytes})

	if err := os.WriteFile(filepath.Join(dir, "server.crt"), serverCertPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "server.key"), serverKeyPEM, 0600); err != nil {
		return err
	}

	clientCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCert.Certificate[0]})
	clientKeyBytes, err := x509.MarshalPKCS8PrivateKey(clientCert.PrivateKey)
	if err != nil {
		return err
	}
	clientKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: clientKeyBytes})

	if err := os.WriteFile(filepath.Join(dir, "client.crt"), clientCertPEM, 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "client.key"), clientKeyPEM, 0600)
}
```

Add `"path/filepath"` to imports.

**Step 3: Build**

```bash
go build ./...
```

Fix any compile errors.

**Step 4: Run the TLS integration test**

```bash
go test ./tests/... -run TestTLSConnection -v -timeout 30s
```

Expected: PASS (the proxy now sends Connection.Start in response to the protocol header)

**Step 5: Commit**

```bash
git add tests/integration_tls_test.go tests/certs.go
git commit -m "fix: integration tests use t.TempDir(), guard conn.Write on nil, fix PEM types, test actual AMQP response"
```

---

## Task 13: Fix `TestProxyIntegration` — Make It Test Something Real

**Files:**
- Modify: `tests/integration_test.go`

The test currently dials TCP and calls it a day. Update it to verify the proxy responds with a Connection.Start frame.

**Step 1: Replace `tests/integration_test.go`**

```go
package tests

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timsweb/amqplex/config"
	"github.com/timsweb/amqplex/proxy"
)

func TestProxyIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15673,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 5,
	}

	p, err := proxy.NewProxy(cfg)
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()

	var conn net.Conn
	for i := 0; i < 10; i++ {
		conn, err = net.Dial("tcp", "localhost:15673")
		if err == nil {
			break
		}
		time.Sleep(time.Duration(i+1) * 50 * time.Millisecond)
	}
	require.NoError(t, err)
	defer conn.Close()

	// Send AMQP protocol header
	_, err = conn.Write([]byte("AMQP\x00\x00\x09\x01"))
	require.NoError(t, err)

	// Expect Connection.Start response within 2 seconds
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	require.Greater(t, n, 7)

	// Frame type = 1 (method frame)
	assert.Equal(t, byte(1), buf[0])
	// Channel = 0
	assert.Equal(t, byte(0), buf[1])
	assert.Equal(t, byte(0), buf[2])
	// Method: class=10 (connection), method=10 (start)
	assert.Equal(t, byte(0), buf[7])
	assert.Equal(t, byte(10), buf[8])
	assert.Equal(t, byte(0), buf[9])
	assert.Equal(t, byte(10), buf[10])
}
```

**Step 2: Build**

```bash
go build ./...
```

**Step 3: Commit**

```bash
git add tests/integration_test.go
git commit -m "test: integration test now verifies proxy sends Connection.Start"
```

---

## Task 14: Run All Tests and Fix Any Remaining Issues

**Step 1: Run full test suite (excluding integration tests that need live RabbitMQ)**

```bash
go test -short ./... -v 2>&1 | tee test-output.txt
```

Expected: All non-integration tests pass.

**Step 2: Run race detector**

```bash
go test -short -race ./... 2>&1
```

Expected: No races detected.

**Step 3: Run vet**

```bash
go vet ./...
```

Expected: No warnings.

**Step 4: Fix any failures**

If any tests fail, read the failure output and fix. Common issues to look for:
- Import cycles introduced by `proxy/upstream.go` importing from `proxy` package (shouldn't happen, it's the same package)
- The `proxy.go` importing `tlsutil` — check the import path matches `go.mod`
- `serializeConnectionTuneOk` in `upstream.go` referencing `serializeConnectionTunePayload` from `connection.go` — both are in package `proxy`, so fine

**Step 5: Final commit**

```bash
git add -p  # review any remaining changes
git commit -m "fix: all tests pass with race detector"
```

---

## Verification Checklist

- [ ] `go build ./...` succeeds with no errors
- [ ] `go vet ./...` produces no warnings
- [ ] `go test -short -race ./...` passes with no races
- [ ] `TestSerializeConnectionStart` passes
- [ ] `TestSendConnectionTunePayload` passes
- [ ] `TestParseConnectionOpen` passes
- [ ] `TestUnmapChannelCleansClientChannels` passes
- [ ] `TestSafeChannelsPerConnection` passes
- [ ] `TestDialUpstream` passes
- [ ] `TestProxyClientToUpstreamChannelRemapping` passes
- [ ] `TestProxyUpstreamToClientChannelRemapping` passes
- [ ] `TestTLSConnection` passes (proxy responds with Connection.Start)
- [ ] `TestProxyIntegration` passes (proxy responds with Connection.Start over plain TCP)
- [ ] No hardcoded `guest/guest//` credentials in production code path
- [ ] `FrameProxy` writes to real `io.Writer` fields, not a `bytes.Buffer`
- [ ] `UnmapChannel` removes from all three maps
- [ ] `SafeChannels` lives on `PooledConnection`, not `ConnectionPool`
- [ ] `PooledConnection.ChannelMappings` dead field removed
- [ ] Duplicate `AMQPProtocolHeader` constant removed
- [ ] `ClientAuth` set in `listener.go` when CA cert is configured
- [ ] `Stop()` closes the listener
- [ ] Cert PEM types use `"PRIVATE KEY"` for PKCS#8 keys
- [ ] `t.TempDir()` used for test cert files
