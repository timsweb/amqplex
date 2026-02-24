# Publish Buffer Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Buffer Basic.Publish messages on ManagedUpstream during upstream reconnect windows so short-lived publishers (PHP fire-and-forget) complete their session successfully and messages drain to RabbitMQ once the upstream reconnects.

**Architecture:** A bounded `[]bufferedMessage` slice (protected by `ManagedUpstream.mu`) accumulates complete multi-frame publish messages when `conn == nil`. `handleConnection` sends a synthetic `Channel.OpenOk` during reconnect so the client gets a usable channel without the upstream. On reconnect, `drainPublishBuffer` opens a dedicated drain channel, replays all buffered messages in order, and clears the buffer before launching the normal read loop.

**Tech Stack:** Go standard library only; no new dependencies. Tests use `net.Pipe` / `bufio` fakes as in the existing test suite.

---

## Context: Key files

- `proxy/managed_upstream.go` — owns the upstream conn, reconnect loop, channel allocation
- `proxy/proxy.go` — `handleConnection` reads client frames and writes to upstream
- `proxy/connection.go` — AMQP frame helpers (`isChannelOpen`, `serializeConnectionClose`, etc.)
- `proxy/frame.go` — `Frame` struct, `ParseFrame`, `WriteFrame`
- `config/config.go` — `Config` struct and viper bindings
- `proxy/managed_upstream_test.go` — unit tests; see `newTestManagedUpstream`, `upstreamPipe`, `startedUpstream`
- `proxy/test_helpers_test.go` — `logCapture`, `newCapture`, `discardLogger`

---

## Task 1: Add `PublishBufferSize` config field

**Files:**
- Modify: `config/config.go`

**Step 1: Write the failing test**

Add to `config/config_test.go` (create the file if it doesn't exist — check first with `ls config/`):

```go
func TestPublishBufferSizeDefault(t *testing.T) {
    cfg, err := LoadConfig("", "")
    require.NoError(t, err)
    assert.Equal(t, 0, cfg.PublishBufferSize)
}
```

Run: `go test ./config/ -run TestPublishBufferSizeDefault -v`
Expected: FAIL — `cfg.PublishBufferSize` field doesn't exist yet.

**Step 2: Add the field to `Config` struct** (after `PoolCleanupInterval`):

```go
PublishBufferSize int // 0 = disabled; max messages to buffer during reconnect
```

**Step 3: Add the viper default** (in `LoadConfig`, after the `pool.cleanup_interval` default):

```go
v.SetDefault("pool.publish_buffer_size", 0)
```

**Step 4: Add the mapping** (in the `cfg := &Config{...}` block, after `PoolCleanupInterval`):

```go
PublishBufferSize: v.GetInt("pool.publish_buffer_size"),
```

**Step 5: Run test to verify it passes**

Run: `go test ./config/ -v`
Expected: PASS

**Step 6: Commit**

```bash
git add config/config.go config/config_test.go
git commit -m "feat(config): add PublishBufferSize field"
```

---

## Task 2: `bufferedMessage` type + buffer fields + methods on ManagedUpstream

**Files:**
- Modify: `proxy/managed_upstream.go`
- Modify: `proxy/managed_upstream_test.go`

### Background

`bufferedMessage` holds the three frame types that make up one AMQP publish:

```
Client → proxy: [Basic.Publish method frame] [content header frame] [body frame(s)]
```

All three must be buffered atomically so drain can replay them in correct AMQP order.

### Step 1: Write failing tests

Add to `proxy/managed_upstream_test.go`:

```go
func TestIsReconnecting_TrueWhenConnNil(t *testing.T) {
    m := newTestManagedUpstream(10)
    // conn is nil by default in newTestManagedUpstream
    assert.True(t, m.IsReconnecting())
}

func TestIsReconnecting_FalseWhenConnSet(t *testing.T) {
    m, _ := startedUpstream(t)
    assert.False(t, m.IsReconnecting())
}

func TestIsBufferFull_FalseWhenDisabled(t *testing.T) {
    m := newTestManagedUpstream(10)
    m.bufferSize = 0
    assert.False(t, m.IsBufferFull())
}

func TestIsBufferFull_FalseWhenSpaceAvailable(t *testing.T) {
    m := newTestManagedUpstream(10)
    m.bufferSize = 5
    assert.False(t, m.IsBufferFull())
}

func TestIsBufferFull_TrueWhenAtCapacity(t *testing.T) {
    m := newTestManagedUpstream(10)
    m.bufferSize = 2
    m.publishBuffer = []bufferedMessage{{}, {}}
    assert.True(t, m.IsBufferFull())
}

func TestBufferPublish_AcceptsWhenSpaceAvailable(t *testing.T) {
    m := newTestManagedUpstream(10)
    m.bufferSize = 5
    msg := bufferedMessage{
        method: &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{0, 60, 0, 40}},
        header: &Frame{Type: FrameTypeHeader, Channel: 1, Payload: make([]byte, 14)},
    }
    ok := m.BufferPublish(msg)
    assert.True(t, ok)
    m.mu.Lock()
    assert.Len(t, m.publishBuffer, 1)
    m.mu.Unlock()
}

func TestBufferPublish_RejectsWhenFull(t *testing.T) {
    m := newTestManagedUpstream(10)
    m.bufferSize = 1
    m.publishBuffer = []bufferedMessage{{}}
    msg := bufferedMessage{method: &Frame{}}
    ok := m.BufferPublish(msg)
    assert.False(t, ok)
    m.mu.Lock()
    assert.Len(t, m.publishBuffer, 1) // unchanged
    m.mu.Unlock()
}

func TestBufferPublish_DisabledWhenSizeZero(t *testing.T) {
    m := newTestManagedUpstream(10)
    m.bufferSize = 0
    ok := m.BufferPublish(bufferedMessage{method: &Frame{}})
    assert.False(t, ok)
}
```

Run: `go test ./proxy/ -run "TestIsReconnecting|TestIsBufferFull|TestBufferPublish" -v`
Expected: FAIL — types and methods don't exist yet.

### Step 2: Add `bufferedMessage` struct to `managed_upstream.go`

Add near the top of the file, after the `channelEntry` struct:

```go
// bufferedMessage holds the complete set of AMQP frames for one Basic.Publish
// message: the method frame, the content header, and zero or more body frames.
// All three are required to replay the publish to a new upstream connection.
type bufferedMessage struct {
    method *Frame
    header *Frame
    bodies []*Frame
}
```

### Step 3: Add fields to `ManagedUpstream` struct

Add after `pendingClose`:

```go
publishBuffer []bufferedMessage // messages buffered during reconnect; protected by mu
bufferSize    int               // 0 = disabled; max len of publishBuffer
```

### Step 4: Fix `newTestManagedUpstream` to initialise `pendingClose`

The existing helper is missing `pendingClose`. Update it:

```go
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
```

### Step 5: Add the three new methods

Add after `ScheduleChannelClose`:

```go
// IsReconnecting reports whether the upstream connection is currently nil
// (i.e. the upstream is in the middle of a reconnect cycle).
func (m *ManagedUpstream) IsReconnecting() bool {
    m.mu.Lock()
    defer m.mu.Unlock()
    return m.conn == nil
}

// IsBufferFull reports whether the publish buffer has reached its capacity.
// Always returns false when bufferSize is 0 (buffering disabled).
func (m *ManagedUpstream) IsBufferFull() bool {
    if m.bufferSize == 0 {
        return false
    }
    m.mu.Lock()
    defer m.mu.Unlock()
    return len(m.publishBuffer) >= m.bufferSize
}

// BufferPublish appends msg to the publish buffer. Returns true if the message
// was accepted, false if buffering is disabled or the buffer is full.
func (m *ManagedUpstream) BufferPublish(msg bufferedMessage) bool {
    if m.bufferSize == 0 {
        return false
    }
    m.mu.Lock()
    defer m.mu.Unlock()
    if len(m.publishBuffer) >= m.bufferSize {
        return false
    }
    m.publishBuffer = append(m.publishBuffer, msg)
    if m.logger != nil {
        m.logger.Debug("publish buffered",
            slog.Int("buffer_depth", len(m.publishBuffer)),
            slog.String("user", m.username),
            slog.String("vhost", m.vhost),
        )
    }
    return true
}
```

### Step 6: Wire `bufferSize` into `getOrCreateManagedUpstream` in `proxy.go`

In the `m := &ManagedUpstream{...}` literal in `getOrCreateManagedUpstream`, add:

```go
bufferSize:    p.config.PublishBufferSize,
```

### Step 7: Run tests

Run: `go test ./proxy/ -run "TestIsReconnecting|TestIsBufferFull|TestBufferPublish" -v`
Expected: PASS

Run: `go test ./... -race`
Expected: all pass, no races.

**Step 8: Commit**

```bash
git add proxy/managed_upstream.go proxy/managed_upstream_test.go proxy/proxy.go
git commit -m "feat(proxy): add publish buffer fields and methods to ManagedUpstream"
```

---

## Task 3: AMQP frame helpers

**Files:**
- Modify: `proxy/connection.go`
- Modify: `proxy/frame.go`
- Modify: `proxy/connection_test.go`

### Step 1: Write failing tests

Add to `proxy/connection_test.go`:

```go
func TestIsBasicPublish(t *testing.T) {
    // Basic.Publish: class=60 (0x00,0x3C), method=40 (0x00,0x28)
    frame := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{0, 60, 0, 40, 0, 0}}
    assert.True(t, isBasicPublish(frame))

    // Basic.Deliver (method=60) is not Basic.Publish
    other := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{0, 60, 0, 60}}
    assert.False(t, isBasicPublish(other))

    // Wrong frame type
    header := &Frame{Type: FrameTypeHeader, Channel: 1, Payload: []byte{0, 60, 0, 40}}
    assert.False(t, isBasicPublish(header))
}

func TestIsChannelOpenOk(t *testing.T) {
    // Channel.OpenOk: class=20, method=11
    frame := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{0, 20, 0, 11, 0, 0, 0, 0}}
    assert.True(t, isChannelOpenOk(frame))

    // Channel.Open (method=10) is not Channel.OpenOk
    open := &Frame{Type: FrameTypeMethod, Channel: 1, Payload: []byte{0, 20, 0, 10}}
    assert.False(t, isChannelOpenOk(open))
}

func TestSerializeChannelOpenOk(t *testing.T) {
    payload := serializeChannelOpenOk()
    // Must be at least 4 bytes (method header) + 4 bytes (channel-id longstr)
    assert.GreaterOrEqual(t, len(payload), 8)
    // class=20, method=11
    assert.Equal(t, uint16(20), binary.BigEndian.Uint16(payload[0:2]))
    assert.Equal(t, uint16(11), binary.BigEndian.Uint16(payload[2:4]))
}
```

Add to `proxy/frame_test.go` (or a new `proxy/frame_helpers_test.go` file — check what exists):

```go
func TestParseBodySizeFromHeader(t *testing.T) {
    payload := make([]byte, 14)
    // class-id=60 at [0:2], weight=0 at [2:4], body-size=1234 at [4:12]
    binary.BigEndian.PutUint16(payload[0:2], 60)
    binary.BigEndian.PutUint64(payload[4:12], 1234)
    assert.Equal(t, uint64(1234), parseBodySizeFromHeader(payload))
}

func TestParseBodySizeFromHeader_TooShort(t *testing.T) {
    assert.Equal(t, uint64(0), parseBodySizeFromHeader([]byte{0, 1, 2}))
}
```

Run: `go test ./proxy/ -run "TestIsBasicPublish|TestIsChannelOpenOk|TestSerializeChannelOpenOk|TestParseBodySize" -v`
Expected: FAIL

### Step 2: Add helpers to `connection.go`

Add after `isChannelCloseOk`:

```go
// isBasicPublish returns true for Basic.Publish method frames (class=60, method=40).
func isBasicPublish(frame *Frame) bool {
    if frame.Type != FrameTypeMethod || len(frame.Payload) < 4 {
        return false
    }
    return frame.Payload[0] == 0 && frame.Payload[1] == 60 &&
        frame.Payload[2] == 0 && frame.Payload[3] == 40
}

// isChannelOpenOk returns true for Channel.OpenOk (class=20, method=11) frames.
func isChannelOpenOk(frame *Frame) bool {
    if frame.Type != FrameTypeMethod || len(frame.Payload) < 4 {
        return false
    }
    return frame.Payload[0] == 0 && frame.Payload[1] == 20 &&
        frame.Payload[2] == 0 && frame.Payload[3] == 11
}

// serializeChannelOpenOk builds a Channel.OpenOk payload.
// Used to send a synthetic response to clients when the upstream is reconnecting.
func serializeChannelOpenOk() []byte {
    header := SerializeMethodHeader(&MethodHeader{ClassID: 20, MethodID: 11})
    return append(header, 0, 0, 0, 0) // channel-id longstring (empty, 4-byte length prefix)
}
```

### Step 3: Add `parseBodySizeFromHeader` to `frame.go`

Add after `SerializeMethodHeader`:

```go
// parseBodySizeFromHeader extracts the body-size field from a content header
// frame payload. The AMQP 0-9-1 content header layout is:
//   [0:2]  class-id (uint16)
//   [2:4]  weight   (uint16, always 0)
//   [4:12] body-size (uint64, big-endian)
// Returns 0 if the payload is too short.
func parseBodySizeFromHeader(payload []byte) uint64 {
    if len(payload) < 12 {
        return 0
    }
    return binary.BigEndian.Uint64(payload[4:12])
}
```

### Step 4: Run tests

Run: `go test ./proxy/ -run "TestIsBasicPublish|TestIsChannelOpenOk|TestSerializeChannelOpenOk|TestParseBodySize" -v`
Expected: PASS

Run: `go test ./... -race`
Expected: all pass.

**Step 5: Commit**

```bash
git add proxy/connection.go proxy/connection_test.go proxy/frame.go
git commit -m "feat(proxy): add isBasicPublish, isChannelOpenOk, serializeChannelOpenOk, parseBodySizeFromHeader helpers"
```

---

## Task 4: Buffer mode in `handleConnection`

**Files:**
- Modify: `proxy/proxy.go`

### Background: current `handleConnection` loop structure

```
for {
    ParseFrame → heartbeat? continue → isConnectionClose? return
    isChannelOpen → AllocateChannel + MapChannel
    Remap channel (client → upstream)
    markChannelSafety
    writeFrameToUpstream → error? return
    isChannelClose → ReleaseChannel + UnmapChannel
}
```

You will add a `publishAccumulator` struct (local to `proxy.go`) and modify the loop to:
1. Send synthetic `Channel.OpenOk` when `isChannelOpen` and `managed.IsReconnecting()`
2. Accumulate multi-frame publish messages when `isBasicPublish` and `managed.IsReconnecting()`
3. Disconnect the client when the buffer is full

### Step 1: Write failing test

Add to `proxy/proxy_test.go`:

```go
// TestBufferOverflowDisconnectsClient verifies that when the publish buffer is
// full, connecting clients are rejected with a 503 on their first publish attempt.
// This test injects a pre-full upstream directly to avoid needing a real dial.
func TestBufferOverflowDisconnectsClient(t *testing.T) {
    cfg := &config.Config{
        ListenAddress:    "localhost",
        ListenPort:       15720,
        UpstreamURL:      "amqp://localhost:5672",
        PoolMaxChannels:  65535,
        PublishBufferSize: 1,
    }
    p, err := NewProxy(cfg, discardLogger())
    require.NoError(t, err)

    // Pre-populate a reconnecting upstream with a full buffer.
    key := p.getPoolKey("guest", "guest", "/")
    m := newTestManagedUpstream(65535)
    m.bufferSize = 1
    // Buffer is full: one message already in it.
    m.publishBuffer = []bufferedMessage{{method: &Frame{}}}
    // conn is nil → IsReconnecting() == true
    p.mu.Lock()
    p.upstreams[key] = []*ManagedUpstream{m}
    p.mu.Unlock()

    go p.Start()
    defer p.Stop()
    time.Sleep(10 * time.Millisecond)

    // Connect and complete the AMQP handshake.
    conn, err := net.Dial("tcp", "localhost:15720")
    require.NoError(t, err)
    defer conn.Close()

    r := bufio.NewReader(conn)

    // Protocol header
    _, err = conn.Write([]byte(ProtocolHeader))
    require.NoError(t, err)

    // Connection.Start
    _, err = ParseFrame(r)
    require.NoError(t, err)

    // Connection.Start-OK
    startOkPayload := serializeConnectionStartOk("PLAIN",
        serializeConnectionStartOkResponse("guest", "guest"))
    require.NoError(t, WriteFrame(conn, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: startOkPayload}))

    // Connection.Tune
    _, err = ParseFrame(r)
    require.NoError(t, err)

    // Connection.Tune-OK
    tuneOkPayload := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 31})
    tuneOkPayload = append(tuneOkPayload, 0, 0, 0, 2, 0, 0, 0, 60)
    require.NoError(t, WriteFrame(conn, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: tuneOkPayload}))

    // Connection.Open
    openPayload := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 40})
    openPayload = append(openPayload, serializeShortString("/")...)
    openPayload = append(openPayload, 0, 0)
    require.NoError(t, WriteFrame(conn, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: openPayload}))

    // Connection.Open-OK
    _, err = ParseFrame(r)
    require.NoError(t, err)

    // Channel.Open on channel 1
    chanOpenPayload := SerializeMethodHeader(&MethodHeader{ClassID: 20, MethodID: 10})
    chanOpenPayload = append(chanOpenPayload, 0)
    require.NoError(t, WriteFrame(conn, &Frame{Type: FrameTypeMethod, Channel: 1, Payload: chanOpenPayload}))

    // Should receive synthetic Channel.OpenOk
    f, err := ParseFrame(r)
    require.NoError(t, err)
    assert.Equal(t, FrameTypeMethod, f.Type)
    assert.True(t, isChannelOpenOk(f))

    // Now send Basic.Publish — buffer is full so proxy should disconnect us.
    publishPayload := SerializeMethodHeader(&MethodHeader{ClassID: 60, MethodID: 40})
    publishPayload = append(publishPayload, 0, 0)                        // reserved
    publishPayload = append(publishPayload, serializeShortString("")...) // exchange
    publishPayload = append(publishPayload, serializeShortString("q")...) // routing-key
    publishPayload = append(publishPayload, 0)                           // mandatory+immediate flags
    require.NoError(t, WriteFrame(conn, &Frame{Type: FrameTypeMethod, Channel: 1, Payload: publishPayload}))

    // Proxy must close the connection (any read returns EOF or Connection.Close).
    conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
    buf := make([]byte, 256)
    _, readErr := conn.Read(buf)
    assert.Error(t, readErr, "proxy should close connection when buffer is full")
}
```

Run: `go test ./proxy/ -run TestBufferOverflowDisconnectsClient -v`
Expected: FAIL — `isChannelOpenOk` not called (proxy doesn't send synthetic OpenOk yet).

### Step 2: Add `publishAccumulator` type to `proxy.go`

Add near the top of `proxy.go`, after the imports:

```go
// publishAccumulator collects the multi-frame sequence of an AMQP Basic.Publish
// message (method + content header + body frames) so it can be buffered atomically.
type publishAccumulator struct {
    method   *Frame
    header   *Frame
    bodies   []*Frame
    bodySize uint64 // declared in content header
    bodyRead uint64 // accumulated so far
}
```

### Step 3: Modify `handleConnection` loop

The `handleConnection` loop in `proxy.go` currently starts at the line:
```go
for {
    frame, err := ParseFrame(cc.Reader)
```

Replace the entire loop body with the updated version below. Read the existing loop carefully first — the new version is additive (inserts three new blocks, leaves everything else intact).

```go
var acc *publishAccumulator // non-nil while collecting a multi-frame publish

for {
    frame, err := ParseFrame(cc.Reader)
    if err != nil {
        return
    }

    // Consume client heartbeats — proxy owns the upstream heartbeat.
    if frame.Type == FrameTypeHeartbeat {
        continue
    }

    // Intercept Connection.Close from the client — do not forward to the shared
    // upstream (that would tear down the connection for all other clients).
    // Respond with Connection.Close-OK and exit cleanly.
    if isConnectionClose(frame) {
        _ = WriteFrame(cc.Writer, &Frame{
            Type:    FrameTypeMethod,
            Channel: 0,
            Payload: SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 51}),
        })
        _ = cc.Writer.Flush()
        return
    }

    if isChannelOpen(frame) {
        upstreamID, err := managed.AllocateChannel(frame.Channel, cc)
        if err != nil {
            // No channel capacity — close this client.
            return
        }
        cc.MapChannel(frame.Channel, upstreamID)

        // NEW: when upstream is reconnecting, send a synthetic Channel.OpenOk
        // so the client gets a usable channel without the upstream needing to
        // be available. The channel will be opened for real during drain.
        if managed.IsReconnecting() {
            _ = cc.DeliverFrame(&Frame{
                Type:    FrameTypeMethod,
                Channel: frame.Channel, // client's channel number
                Payload: serializeChannelOpenOk(),
            })
            continue
        }
    }

    // Remap client channel ID to upstream channel ID.
    cc.Mu.RLock()
    upstreamChanID, mapped := cc.ChannelMapping[frame.Channel]
    cc.Mu.RUnlock()

    remapped := *frame
    if mapped {
        remapped.Channel = upstreamChanID
    }

    // Mark channel unsafe if needed.
    markChannelSafety(frame, cc)

    // NEW: if currently accumulating a multi-frame publish, collect header/body.
    if acc != nil {
        switch frame.Type {
        case FrameTypeHeader:
            acc.header = &remapped
            acc.bodySize = parseBodySizeFromHeader(frame.Payload)
            if acc.bodySize == 0 {
                // Zero-length body — message is complete.
                managed.BufferPublish(bufferedMessage{method: acc.method, header: acc.header})
                acc = nil
            }
        case FrameTypeBody:
            bodyCopy := remapped
            acc.bodies = append(acc.bodies, &bodyCopy)
            acc.bodyRead += uint64(len(frame.Payload))
            if acc.bodyRead >= acc.bodySize {
                managed.BufferPublish(bufferedMessage{
                    method: acc.method,
                    header: acc.header,
                    bodies: acc.bodies,
                })
                acc = nil
            }
        default:
            // Unexpected frame type mid-message — abandon accumulation and
            // fall through to normal processing for this frame.
            acc = nil
        }
        if acc != nil || frame.Type == FrameTypeHeader || frame.Type == FrameTypeBody {
            continue // frame consumed by accumulator
        }
    }

    // NEW: start buffering a Basic.Publish when upstream is reconnecting.
    if isBasicPublish(frame) && managed.IsReconnecting() {
        if managed.IsBufferFull() {
            // Buffer overflow — disconnect this client.
            return
        }
        methodCopy := remapped
        acc = &publishAccumulator{method: &methodCopy}
        continue
    }

    if err := managed.writeFrameToUpstream(&remapped); err != nil {
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
```

> **Note on the accumulator `continue` logic:** The switch handles `FrameTypeHeader` and `FrameTypeBody` with explicit `continue` via the outer check. When the `default` case fires (abandons accumulation), we fall through to normal frame processing (writeFrameToUpstream, etc.).

### Step 4: Run test

Run: `go test ./proxy/ -run TestBufferOverflowDisconnectsClient -v`
Expected: PASS

Run: `go test ./... -race`
Expected: all pass.

**Step 5: Commit**

```bash
git add proxy/proxy.go
git commit -m "feat(proxy): buffer Basic.Publish during upstream reconnect; synthetic Channel.OpenOk"
```

---

## Task 5: Drain buffered messages on reconnect

**Files:**
- Modify: `proxy/managed_upstream.go`
- Modify: `proxy/managed_upstream_test.go`

### Background

`drainPublishBuffer` is called from `reconnectLoop` after a new `*UpstreamConn` is established but **before** `readLoop` is launched. It:

1. Opens upstream channel 1 as a dedicated drain channel (real `Channel.Open` / wait for `Channel.OpenOk`).
2. Replays all buffered messages on channel 1 (remapping frame channel IDs).
3. Sends `Channel.Close` for the drain channel and calls `ScheduleChannelClose(1)` so the `readLoop` will release it when `Channel.CloseOk` arrives.
4. Clears the buffer on success; on write failure, keeps un-replayed messages for the next reconnect attempt.

### Step 1: Write failing tests

Add to `proxy/managed_upstream_test.go`:

```go
func TestDrainPublishBuffer_Empty(t *testing.T) {
    // No messages in buffer — drain should be a no-op.
    proxyConn, serverConn := upstreamPipe(t)
    conn := &UpstreamConn{
        Conn:   proxyConn,
        Reader: bufio.NewReader(proxyConn),
        Writer: bufio.NewWriter(proxyConn),
    }
    m := newTestManagedUpstream(65535)
    m.bufferSize = 10

    m.drainPublishBuffer(conn)

    // Server side should have received nothing.
    serverConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
    buf := make([]byte, 1)
    _, err := serverConn.Read(buf)
    assert.Error(t, err, "no frames expected for empty buffer")

    m.mu.Lock()
    assert.Empty(t, m.publishBuffer, "buffer should remain empty")
    m.mu.Unlock()
}

func TestDrainPublishBuffer_ReplaysMessages(t *testing.T) {
    proxyConn, serverConn := upstreamPipe(t)
    conn := &UpstreamConn{
        Conn:   proxyConn,
        Reader: bufio.NewReader(proxyConn),
        Writer: bufio.NewWriter(proxyConn),
    }

    m := newTestManagedUpstream(65535)
    m.bufferSize = 10

    // Populate buffer with two messages.
    body := []byte("hello")
    headerPayload := make([]byte, 14)
    binary.BigEndian.PutUint64(headerPayload[4:12], uint64(len(body)))

    for i := 0; i < 2; i++ {
        m.publishBuffer = append(m.publishBuffer, bufferedMessage{
            method: &Frame{Type: FrameTypeMethod, Channel: 3, Payload: []byte{0, 60, 0, 40, 0, 0, 0, 5, 'h', 'e', 'l', 'l', 'o'}},
            header: &Frame{Type: FrameTypeHeader, Channel: 3, Payload: headerPayload},
            bodies: []*Frame{
                {Type: FrameTypeBody, Channel: 3, Payload: body},
            },
        })
    }

    // Run drain in a goroutine (it will block waiting for Channel.OpenOk).
    done := make(chan struct{})
    go func() {
        defer close(done)
        m.drainPublishBuffer(conn)
    }()

    // Server-side: respond to Channel.Open with Channel.OpenOk.
    sr := bufio.NewReader(serverConn)
    sw := bufio.NewWriter(serverConn)

    // 1. Receive Channel.Open
    f, err := ParseFrame(sr)
    require.NoError(t, err)
    assert.Equal(t, FrameTypeMethod, f.Type)
    assert.Equal(t, uint16(1), f.Channel)
    assert.Equal(t, uint16(20), binary.BigEndian.Uint16(f.Payload[0:2])) // class=20
    assert.Equal(t, uint16(10), binary.BigEndian.Uint16(f.Payload[2:4])) // method=10

    // 2. Send Channel.OpenOk
    openOkPayload := serializeChannelOpenOk()
    require.NoError(t, WriteFrame(sw, &Frame{Type: FrameTypeMethod, Channel: 1, Payload: openOkPayload}))
    require.NoError(t, sw.Flush())

    // 3. Receive 2 × (method + header + body) frames on channel 1
    for i := 0; i < 2; i++ {
        method, err := ParseFrame(sr)
        require.NoError(t, err, "message %d method", i)
        assert.Equal(t, uint16(1), method.Channel)
        assert.True(t, isBasicPublish(method))

        hdr, err := ParseFrame(sr)
        require.NoError(t, err, "message %d header", i)
        assert.Equal(t, uint16(1), hdr.Channel)
        assert.Equal(t, FrameTypeHeader, hdr.Type)

        bd, err := ParseFrame(sr)
        require.NoError(t, err, "message %d body", i)
        assert.Equal(t, uint16(1), bd.Channel)
        assert.Equal(t, body, bd.Payload)
    }

    // 4. Receive Channel.Close
    close, err := ParseFrame(sr)
    require.NoError(t, err)
    assert.True(t, isChannelClose(close))
    assert.Equal(t, uint16(1), close.Channel)

    select {
    case <-done:
    case <-time.After(time.Second):
        t.Fatal("drainPublishBuffer did not complete")
    }

    m.mu.Lock()
    assert.Empty(t, m.publishBuffer, "buffer must be cleared after successful drain")
    assert.True(t, m.pendingClose[1], "drain channel must be in pendingClose")
    m.mu.Unlock()
}
```

Run: `go test ./proxy/ -run "TestDrainPublishBuffer" -v`
Expected: FAIL — `drainPublishBuffer` doesn't exist.

### Step 2: Add `drainPublishBuffer` to `managed_upstream.go`

Add after `markStoppedIfIdle`:

```go
// drainPublishBuffer replays all buffered publish messages to the upstream on
// a dedicated drain channel (channel 1). Must be called after a new upstream
// connection is established but before readLoop is launched.
//
// On write failure, un-replayed messages are prepended back to publishBuffer so
// they will be retried on the next reconnect. On full success, publishBuffer is
// cleared and channel 1 is placed into pendingClose so readLoop releases it when
// Channel.CloseOk arrives.
func (m *ManagedUpstream) drainPublishBuffer(conn *UpstreamConn) {
    m.mu.Lock()
    if len(m.publishBuffer) == 0 {
        m.mu.Unlock()
        return
    }
    // Snapshot the buffer. New publishes arriving during drain go to the tail of
    // m.publishBuffer; we prepend any failures before them on error.
    msgs := append([]bufferedMessage(nil), m.publishBuffer...)
    m.mu.Unlock()

    if m.logger != nil {
        m.logger.Info("draining publish buffer",
            slog.Int("count", len(msgs)),
            slog.String("user", m.username),
            slog.String("vhost", m.vhost),
        )
    }

    // Reserve drain channel 1 so AllocateChannel won't hand it to a client
    // while drain is in progress.
    m.mu.Lock()
    m.usedChannels[1] = true
    m.mu.Unlock()

    // Helper to put msgs[from:] back at the front of the buffer and release the
    // drain channel reservation on any error path.
    abort := func(from int) {
        m.mu.Lock()
        tail := append([]bufferedMessage(nil), m.publishBuffer...)
        m.publishBuffer = append(msgs[from:], tail...)
        delete(m.usedChannels, 1)
        m.mu.Unlock()
    }

    // Send Channel.Open for the drain channel.
    openPayload := SerializeMethodHeader(&MethodHeader{ClassID: 20, MethodID: 10})
    openPayload = append(openPayload, 0) // out-of-band shortstring (empty)
    if err := WriteFrame(conn.Writer, &Frame{Type: FrameTypeMethod, Channel: 1, Payload: openPayload}); err != nil {
        abort(0)
        return
    }
    if err := conn.Writer.Flush(); err != nil {
        abort(0)
        return
    }

    // Wait for Channel.OpenOk (5-second timeout).
    conn.Conn.SetReadDeadline(time.Now().Add(5 * time.Second))
    openOk, err := ParseFrame(conn.Reader)
    conn.Conn.SetReadDeadline(time.Time{})
    if err != nil || !isChannelOpenOk(openOk) {
        if m.logger != nil {
            m.logger.Warn("drain channel open failed — messages preserved",
                slog.String("user", m.username),
                slog.String("vhost", m.vhost),
            )
        }
        abort(0)
        return
    }

    // Replay each buffered message on the drain channel.
    for i, msg := range msgs {
        writeMsg := func() error {
            method := *msg.method
            method.Channel = 1
            if err := WriteFrame(conn.Writer, &method); err != nil {
                return err
            }
            if msg.header != nil {
                hdr := *msg.header
                hdr.Channel = 1
                if err := WriteFrame(conn.Writer, &hdr); err != nil {
                    return err
                }
            }
            for _, body := range msg.bodies {
                b := *body
                b.Channel = 1
                if err := WriteFrame(conn.Writer, &b); err != nil {
                    return err
                }
            }
            return nil
        }
        if err := writeMsg(); err != nil {
            abort(i) // keep msgs[i:] (message i may be partially written — accept dup risk)
            return
        }
    }

    if err := conn.Writer.Flush(); err != nil {
        abort(0) // keep all messages; flush failed so nothing was received by broker
        return
    }

    // Close the drain channel. readLoop will receive Channel.CloseOk and release
    // the slot via the existing pendingClose mechanism.
    closePayload := SerializeMethodHeader(&MethodHeader{ClassID: 20, MethodID: 40})
    closePayload = append(closePayload, 0, 0)                       // reply-code = 0
    closePayload = append(closePayload, serializeShortString("")...) // reply-text = ""
    closePayload = append(closePayload, 0, 0, 0, 0)                 // class-id=0, method-id=0
    _ = WriteFrame(conn.Writer, &Frame{Type: FrameTypeMethod, Channel: 1, Payload: closePayload})
    _ = conn.Writer.Flush()
    m.ScheduleChannelClose(1) // marks pendingClose[1], removes from channelOwners

    // Success: clear the snapshot messages from the buffer.
    m.mu.Lock()
    // publishBuffer may have grown during drain — keep only the tail (new arrivals).
    if len(m.publishBuffer) >= len(msgs) {
        m.publishBuffer = m.publishBuffer[len(msgs):]
    } else {
        m.publishBuffer = m.publishBuffer[:0]
    }
    m.mu.Unlock()

    if m.logger != nil {
        m.logger.Info("publish buffer drained",
            slog.Int("count", len(msgs)),
            slog.String("user", m.username),
            slog.String("vhost", m.vhost),
        )
    }
}
```

### Step 3: Call `drainPublishBuffer` from `reconnectLoop`

In `reconnectLoop`, after `m.conn = conn` is set and channel state is reset, add the drain call **before** `go m.readLoop()`:

```go
m.mu.Lock()
m.conn = conn
// Reset channel state — all clients were torn down on failure
m.usedChannels = make(map[uint16]bool)
m.channelOwners = make(map[uint16]channelEntry)
m.pendingClose = make(map[uint16]bool)
m.clients = make([]clientWriter, 0)
m.lastEmptyTime = time.Now() // no clients yet after reconnect
m.mu.Unlock()

// Drain any messages buffered during the reconnect window before
// resuming normal operation.
m.drainPublishBuffer(conn)   // ← ADD THIS LINE

go m.readLoop()
```

### Step 4: Run tests

Run: `go test ./proxy/ -run "TestDrainPublishBuffer" -v`
Expected: PASS

Run: `go test ./... -race`
Expected: all pass.

**Step 5: Commit**

```bash
git add proxy/managed_upstream.go proxy/managed_upstream_test.go
git commit -m "feat(proxy): drain buffered publishes to upstream on reconnect"
```

---

## Task 6: Integration test — publish buffer end-to-end

**Files:**
- Modify: `proxy/proxy_test.go`

This test uses a real in-process TCP upstream (not RabbitMQ) to verify the full lifecycle:
publish during outage → reconnect → drain → messages received by new upstream.

### Step 1: Write the failing test

Add to `proxy/proxy_test.go`:

```go
// TestPublishBufferDrainedAfterReconnect verifies the full buffer lifecycle:
//   1. Client connects; upstream is live.
//   2. Upstream dies; handleUpstreamFailure fires.
//   3. New client connects; publishes 2 messages (goes to buffer via synthetic OpenOk).
//   4. Fake upstream comes back; reconnectLoop fires; drainPublishBuffer replays messages.
//   5. New upstream receives Channel.Open + 2 × (method+header+body) + Channel.Close.
func TestPublishBufferDrainedAfterReconnect(t *testing.T) {
    // ── set up a fake upstream ──────────────────────────────────────────────────
    ln, err := net.Listen("tcp", "127.0.0.1:0")
    require.NoError(t, err)
    t.Cleanup(func() { ln.Close() })

    upstreamAddr := ln.Addr().String()

    // Track connections to the fake upstream.
    connCh := make(chan net.Conn, 4)
    go func() {
        for {
            c, err := ln.Accept()
            if err != nil {
                return
            }
            connCh <- c
        }
    }()

    // ── start proxy pointing at fake upstream ──────────────────────────────────
    cfg := &config.Config{
        ListenAddress:    "localhost",
        ListenPort:       15730,
        UpstreamURL:      "amqp://" + upstreamAddr + "/",
        PoolMaxChannels:  65535,
        PublishBufferSize: 10,
    }
    p, err := NewProxy(cfg, discardLogger())
    require.NoError(t, err)
    go p.Start()
    defer p.Stop()
    time.Sleep(20 * time.Millisecond)

    // ── helper: complete a fake upstream AMQP handshake ────────────────────────
    doUpstreamHandshake := func(sc net.Conn) {
        t.Helper()
        r := bufio.NewReader(sc)
        w := bufio.NewWriter(sc)

        // Read protocol header
        hdr := make([]byte, 8)
        _, err := io.ReadFull(r, hdr)
        require.NoError(t, err)

        // Send Connection.Start (minimal)
        startPayload := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 10})
        startPayload = append(startPayload, 0, 9)          // version 0,9
        startPayload = append(startPayload, 0, 0, 0, 0)    // server-properties (empty table)
        startPayload = append(startPayload, 0, 0, 0, 5)    // mechanisms len
        startPayload = append(startPayload, []byte("PLAIN")...)
        startPayload = append(startPayload, 0, 0, 0, 5)    // locales len
        startPayload = append(startPayload, []byte("en_US")...)
        require.NoError(t, WriteFrame(w, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: startPayload}))
        require.NoError(t, w.Flush())

        // Read Connection.Start-OK
        _, err = ParseFrame(r)
        require.NoError(t, err)

        // Send Connection.Tune
        tunePayload := serializeConnectionTunePayload()
        require.NoError(t, WriteFrame(w, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: tunePayload}))
        require.NoError(t, w.Flush())

        // Read Connection.Tune-OK
        _, err = ParseFrame(r)
        require.NoError(t, err)

        // Read Connection.Open
        _, err = ParseFrame(r)
        require.NoError(t, err)

        // Send Connection.Open-OK
        openOkPayload := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 41})
        openOkPayload = append(openOkPayload, 0)
        require.NoError(t, WriteFrame(w, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: openOkPayload}))
        require.NoError(t, w.Flush())
    }

    // ── step 1: accept initial upstream connection ─────────────────────────────
    select {
    case sc := <-connCh:
        doUpstreamHandshake(sc)
        // Kill the upstream to trigger reconnect.
        sc.Close()
    case <-time.After(2 * time.Second):
        t.Fatal("proxy did not connect to fake upstream")
    }

    // Wait for proxy to enter reconnect mode.
    time.Sleep(50 * time.Millisecond)

    // ── step 2: connect a client and publish 2 messages ───────────────────────
    clientConn, err := net.Dial("tcp", "localhost:15730")
    require.NoError(t, err)
    defer clientConn.Close()

    cr := bufio.NewReader(clientConn)

    // Full AMQP client handshake with the proxy.
    _, err = clientConn.Write([]byte(ProtocolHeader))
    require.NoError(t, err)
    _, err = ParseFrame(cr) // Connection.Start
    require.NoError(t, err)
    startOkPayload := serializeConnectionStartOk("PLAIN", serializeConnectionStartOkResponse("guest", "guest"))
    require.NoError(t, WriteFrame(clientConn, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: startOkPayload}))
    _, err = ParseFrame(cr) // Connection.Tune
    require.NoError(t, err)
    tuneOk := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 31})
    tuneOk = append(tuneOk, 0, 0, 0, 2, 0, 0, 0, 60)
    require.NoError(t, WriteFrame(clientConn, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: tuneOk}))
    openPayload := SerializeMethodHeader(&MethodHeader{ClassID: 10, MethodID: 40})
    openPayload = append(openPayload, serializeShortString("/")...)
    openPayload = append(openPayload, 0, 0)
    require.NoError(t, WriteFrame(clientConn, &Frame{Type: FrameTypeMethod, Channel: 0, Payload: openPayload}))
    _, err = ParseFrame(cr) // Connection.OpenOk
    require.NoError(t, err)

    // Channel.Open — expect synthetic Channel.OpenOk.
    chanOpen := SerializeMethodHeader(&MethodHeader{ClassID: 20, MethodID: 10})
    chanOpen = append(chanOpen, 0)
    require.NoError(t, WriteFrame(clientConn, &Frame{Type: FrameTypeMethod, Channel: 1, Payload: chanOpen}))
    openOkFrame, err := ParseFrame(cr)
    require.NoError(t, err)
    require.True(t, isChannelOpenOk(openOkFrame), "expected synthetic Channel.OpenOk")

    // Publish 2 messages (fire-and-forget).
    publishMsg := func(routing string, body []byte) {
        t.Helper()
        // Method frame: Basic.Publish
        pubPayload := SerializeMethodHeader(&MethodHeader{ClassID: 60, MethodID: 40})
        pubPayload = append(pubPayload, 0, 0)                              // reserved
        pubPayload = append(pubPayload, serializeShortString("")...)        // exchange
        pubPayload = append(pubPayload, serializeShortString(routing)...)   // routing-key
        pubPayload = append(pubPayload, 0)                                 // mandatory+immediate
        require.NoError(t, WriteFrame(clientConn, &Frame{Type: FrameTypeMethod, Channel: 1, Payload: pubPayload}))

        // Content header
        headerPayload := make([]byte, 14)
        binary.BigEndian.PutUint16(headerPayload[0:2], 60)             // class-id
        binary.BigEndian.PutUint64(headerPayload[4:12], uint64(len(body))) // body-size
        require.NoError(t, WriteFrame(clientConn, &Frame{Type: FrameTypeHeader, Channel: 1, Payload: headerPayload}))

        // Body
        require.NoError(t, WriteFrame(clientConn, &Frame{Type: FrameTypeBody, Channel: 1, Payload: body}))
    }

    publishMsg("test.key", []byte("msg1"))
    publishMsg("test.key", []byte("msg2"))

    // ── step 3: bring fake upstream back; observe drain ────────────────────────
    var drainConn net.Conn
    select {
    case drainConn = <-connCh:
    case <-time.After(3 * time.Second):
        t.Fatal("proxy did not reconnect to fake upstream")
    }
    defer drainConn.Close()

    doUpstreamHandshake(drainConn)

    dr := bufio.NewReader(drainConn)

    // Drain: expect Channel.Open on channel 1.
    f, err := ParseFrame(dr)
    require.NoError(t, err)
    assert.Equal(t, uint16(1), f.Channel)
    assert.Equal(t, uint16(20), binary.BigEndian.Uint16(f.Payload[0:2]))
    assert.Equal(t, uint16(10), binary.BigEndian.Uint16(f.Payload[2:4]))

    // Respond with Channel.OpenOk.
    dw := bufio.NewWriter(drainConn)
    require.NoError(t, WriteFrame(dw, &Frame{Type: FrameTypeMethod, Channel: 1, Payload: serializeChannelOpenOk()}))
    require.NoError(t, dw.Flush())

    // Receive 2 × (method + header + body) on channel 1.
    bodies := map[string]bool{}
    for i := 0; i < 2; i++ {
        method, err := ParseFrame(dr)
        require.NoError(t, err, "message %d method", i)
        assert.True(t, isBasicPublish(method))
        assert.Equal(t, uint16(1), method.Channel)

        _, err = ParseFrame(dr) // header
        require.NoError(t, err, "message %d header", i)

        body, err := ParseFrame(dr)
        require.NoError(t, err, "message %d body", i)
        bodies[string(body.Payload)] = true
    }
    assert.True(t, bodies["msg1"], "msg1 not received by upstream after drain")
    assert.True(t, bodies["msg2"], "msg2 not received by upstream after drain")

    // Channel.Close for drain channel.
    closeFrame, err := ParseFrame(dr)
    require.NoError(t, err)
    assert.True(t, isChannelClose(closeFrame))
}
```

Note: this test uses `io` (for `io.ReadFull`). Ensure the import is present in `proxy_test.go`.

Run: `go test ./proxy/ -run TestPublishBufferDrainedAfterReconnect -v -timeout 30s`
Expected: FAIL (buffer not wired up yet — but from a later task perspective, PASS).

### Step 2: Run all tests

Run: `go test ./... -race -timeout 60s`
Expected: all pass, no races.

**Step 3: Commit**

```bash
git add proxy/proxy_test.go
git commit -m "test(proxy): end-to-end publish buffer drain after reconnect"
```

---

## Verification

```bash
# All unit tests with race detector
go test ./... -race -count=1 -timeout 60s

# Integration tests (requires Docker Compose)
make integration
```

All packages must pass race-clean. The integration test suite (10 tests) must remain green — the new feature touches `handleConnection` and `reconnectLoop` which affect existing paths.
