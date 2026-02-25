# Structured Logging Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add structured `log/slog` logging throughout AMQProxy so operators can see proxy activity, client connects/disconnects, and upstream events in production.

**Architecture:** Inject `*slog.Logger` into `NewProxy` (and propagate to `ManagedUpstream`). Add `buildLogger()` to `main.go` that reads `LOG_FORMAT` and `LOG_LEVEL` env vars. Tests pass a silent discard logger.

**Tech Stack:** stdlib `log/slog` — no new dependencies.

---

## Context

Working directory: repo root (the `.worktrees/feat-multiplexing` worktree is where this was designed, but check `git branch` first).

Key files:
- `proxy/proxy.go` — `Proxy` struct and `NewProxy`, `handleConnection`, `Stop`
- `proxy/managed_upstream.go` — `ManagedUpstream` struct, `handleUpstreamFailure`, `reconnectLoop`, `AllocateChannel`, `ReleaseChannel`
- `main.go` — entry point; calls `proxy.NewProxy(cfg)`
- `proxy/proxy_test.go` — 2 `NewProxy` callsites
- `tests/integration_test.go` — 1 `NewProxy` callsite
- `tests/integration_tls_test.go` — 2 `NewProxy` callsites
- `tests/integration_concurrency_test.go` — 3 `NewProxy` callsites

Run tests with: `go test ./... -race`

---

## Task 1: Wire logger into Proxy — infra only, no log calls yet

**Goal:** Add `*slog.Logger` to `Proxy`, update `NewProxy` signature, fix all 9 callsites, add `buildLogger()` to `main.go`, and create shared test helpers.

**Files:**
- Modify: `proxy/proxy.go`
- Modify: `main.go`
- Modify: `proxy/proxy_test.go`
- Modify: `tests/integration_test.go`
- Modify: `tests/integration_tls_test.go`
- Modify: `tests/integration_concurrency_test.go`
- Create: `proxy/test_helpers_test.go`
- Create: `tests/helpers_test.go`

### Step 1: Write the failing test (proxy package)

In `proxy/proxy_test.go`, update `TestNewProxy` to pass a logger. This will fail until `NewProxy` signature changes.

```go
// In proxy/proxy_test.go — replace the import block and TestNewProxy
import (
    "io"
    "log/slog"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "github.com/timsweb/amqplex/config"
)

func TestNewProxy(t *testing.T) {
    cfg := &config.Config{
        ListenAddress:   "localhost",
        ListenPort:      5673,
        UpstreamURL:     "amqp://localhost:5672",
        PoolIdleTimeout: 5,
    }

    logger := slog.New(slog.NewTextHandler(io.Discard, nil))
    proxy, err := NewProxy(cfg, logger)
    assert.NoError(t, err)
    assert.NotNil(t, proxy)
    assert.NotNil(t, proxy.listener)
}
```

### Step 2: Run to verify it fails

```bash
go test ./proxy/ -run TestNewProxy -v
```

Expected: compile error — `NewProxy` takes 1 argument, not 2.

### Step 3: Create `proxy/test_helpers_test.go`

```go
package proxy

import (
    "context"
    "io"
    "log/slog"
    "sync"
    "time"
)

// logCapture is a slog.Handler that records all log records for test assertions.
type logCapture struct {
    mu      sync.Mutex
    records []slog.Record
}

func (lc *logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (lc *logCapture) Handle(_ context.Context, r slog.Record) error {
    lc.mu.Lock()
    defer lc.mu.Unlock()
    lc.records = append(lc.records, r.Clone())
    return nil
}

func (lc *logCapture) WithAttrs([]slog.Attr) slog.Handler { return lc }
func (lc *logCapture) WithGroup(string) slog.Handler      { return lc }

// newCapture returns a capture handler and a logger backed by it.
func newCapture() (*logCapture, *slog.Logger) {
    lc := &logCapture{}
    return lc, slog.New(lc)
}

// discardLogger returns a logger that silences all output.
func discardLogger() *slog.Logger {
    return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// messages returns all log message strings recorded so far.
func (lc *logCapture) messages() []string {
    lc.mu.Lock()
    defer lc.mu.Unlock()
    msgs := make([]string, len(lc.records))
    for i, r := range lc.records {
        msgs[i] = r.Message
    }
    return msgs
}

// attrValue returns the value of a named attribute from any recorded log entry.
func (lc *logCapture) attrValue(key string) (slog.Value, bool) {
    lc.mu.Lock()
    defer lc.mu.Unlock()
    for _, r := range lc.records {
        var found slog.Value
        var ok bool
        r.Attrs(func(a slog.Attr) bool {
            if a.Key == key {
                found = a.Value
                ok = true
                return false
            }
            return true
        })
        if ok {
            return found, true
        }
    }
    return slog.Value{}, false
}

// waitForMessage polls until a log message matching msg appears or timeout elapses.
func (lc *logCapture) waitForMessage(msg string, timeout time.Duration) bool {
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        lc.mu.Lock()
        for _, r := range lc.records {
            if r.Message == msg {
                lc.mu.Unlock()
                return true
            }
        }
        lc.mu.Unlock()
        time.Sleep(5 * time.Millisecond)
    }
    return false
}
```

### Step 4: Create `tests/helpers_test.go`

```go
package tests

import (
    "io"
    "log/slog"
)

func discardLogger() *slog.Logger {
    return slog.New(slog.NewTextHandler(io.Discard, nil))
}
```

### Step 5: Update `proxy/proxy.go` — add logger field, update NewProxy

In `proxy/proxy.go`, add `"log/slog"` to imports, add `logger *slog.Logger` field to `Proxy`, update `NewProxy`:

```go
// Proxy struct gains:
type Proxy struct {
    listener    *AMQPListener
    config      *config.Config
    upstreams   map[[32]byte][]*ManagedUpstream
    netListener net.Listener
    mu          sync.RWMutex
    wg          sync.WaitGroup
    logger      *slog.Logger
}

// NewProxy signature:
func NewProxy(cfg *config.Config, logger *slog.Logger) (*Proxy, error) {
    listener, err := NewAMQPListener(cfg)
    if err != nil {
        return nil, fmt.Errorf("failed to create listener: %w", err)
    }
    return &Proxy{
        listener:  listener,
        config:    cfg,
        upstreams: make(map[[32]byte][]*ManagedUpstream),
        logger:    logger,
    }, nil
}
```

### Step 6: Update `main.go` — add buildLogger, pass logger

Replace `"log"` with `"log/slog"`, `"os"`, `"strings"` in imports (some already present).
Add `buildLogger()` function and pass logger to `NewProxy`:

```go
// Add this function to main.go:
func buildLogger() *slog.Logger {
    levelStr := strings.ToLower(os.Getenv("LOG_LEVEL"))
    var level slog.Level
    switch levelStr {
    case "debug":
        level = slog.LevelDebug
    case "warn":
        level = slog.LevelWarn
    default:
        level = slog.LevelInfo
    }

    opts := &slog.HandlerOptions{Level: level}
    var handler slog.Handler
    if strings.ToLower(os.Getenv("LOG_FORMAT")) == "json" {
        handler = slog.NewJSONHandler(os.Stdout, opts)
    } else {
        handler = slog.NewTextHandler(os.Stdout, opts)
    }
    return slog.New(handler)
}
```

In `main()`, replace `proxy.NewProxy(cfg)` with:
```go
logger := buildLogger()
p, err := proxy.NewProxy(cfg, logger)
```

Also update the `log.Printf` and `log.Fatalf` calls to use `logger` (or keep using `log` for pre-proxy startup, which is fine).

### Step 7: Fix all test callsites

**`proxy/proxy_test.go`** — also update `TestGetOrCreateManagedUpstream_ReturnsSameInstance`:
```go
p, err := NewProxy(cfg, discardLogger())
```
(Both calls on lines 19 and 33.)

**`tests/integration_test.go`** line 26:
```go
p, err := proxy.NewProxy(cfg, discardLogger())
```

**`tests/integration_tls_test.go`** lines 42 and 108:
```go
p, err := proxy.NewProxy(cfg, discardLogger())
// and
p, _ := proxy.NewProxy(cfg, discardLogger())
```

**`tests/integration_concurrency_test.go`** lines 28, 86, 136:
```go
p, err := proxy.NewProxy(cfg, discardLogger())
```

### Step 8: Run tests to verify

```bash
go test ./... -race
```

Expected: all tests pass, no compile errors, no race conditions.

### Step 9: Commit

```bash
git add proxy/proxy.go proxy/test_helpers_test.go tests/helpers_test.go \
        proxy/proxy_test.go tests/integration_test.go \
        tests/integration_tls_test.go tests/integration_concurrency_test.go \
        main.go
git commit -m "feat(logging): wire *slog.Logger into Proxy; add buildLogger to main"
```

---

## Task 2: Log proxy and client lifecycle events

**Goal:** Log proxy start/stop and client connect/disconnect events.

**Files:**
- Modify: `proxy/proxy.go`
- Modify: `proxy/test_helpers_test.go` (no changes needed — helpers already there)
- Modify: `proxy/proxy_test.go` (add lifecycle log assertions)

### Step 1: Write failing tests

Add to `proxy/proxy_test.go`:

```go
func TestProxyLogsStartAndStop(t *testing.T) {
    lc, logger := newCapture()
    cfg := &config.Config{
        ListenAddress:   "localhost",
        ListenPort:      15690,
        UpstreamURL:     "amqp://localhost:5672",
        PoolIdleTimeout: 5,
    }
    p, err := NewProxy(cfg, logger)
    require.NoError(t, err)

    started := make(chan struct{})
    go func() {
        close(started)
        _ = p.Start()
    }()
    <-started
    time.Sleep(10 * time.Millisecond) // let Start() reach Accept()

    // Check "proxy started" was logged with addr field
    msgs := lc.messages()
    require.Contains(t, msgs, "proxy started")
    addrVal, ok := lc.attrValue("addr")
    assert.True(t, ok, "expected addr field in 'proxy started' log")
    assert.Contains(t, addrVal.String(), "15690")

    _ = p.Stop()
    assert.Contains(t, lc.messages(), "proxy stopped")
}

func TestClientConnectDisconnectLogged(t *testing.T) {
    lc, logger := newCapture()
    cfg := &config.Config{
        ListenAddress:   "localhost",
        ListenPort:      15691,
        UpstreamURL:     "amqp://localhost:5672",
        PoolIdleTimeout: 5,
    }
    p, err := NewProxy(cfg, logger)
    require.NoError(t, err)

    go p.Start()
    defer p.Stop()
    time.Sleep(10 * time.Millisecond)

    // Connect a raw TCP client and send AMQP header + incomplete handshake
    conn, err := net.Dial("tcp", "localhost:15691")
    require.NoError(t, err)
    _, _ = conn.Write([]byte("AMQP\x00\x00\x09\x01"))
    conn.Close()

    // The client should appear in logs — wait briefly for goroutine to process
    assert.True(t, lc.waitForMessage("client connected", 500*time.Millisecond) ||
        lc.waitForMessage("client rejected", 500*time.Millisecond),
        "expected client connection attempt to be logged")
}
```

### Step 2: Run to verify tests fail

```bash
go test ./proxy/ -run "TestProxyLogsStart|TestClientConnect" -v
```

Expected: FAIL — "proxy started" message not found.

### Step 3: Add log calls to `proxy/proxy.go`

In `Start()`, after the listener is stored, add:
```go
p.logger.Info("proxy started",
    slog.String("addr", fmt.Sprintf("%s:%d", p.config.ListenAddress, p.config.ListenPort)),
)
```

In `Stop()`, after the WaitGroup drains (before `return nil`), add:
```go
p.logger.Info("proxy stopped")
```

In `handleConnection()`, after credentials are extracted (after the `username == ""` guard), add:
```go
remoteAddr := clientConn.RemoteAddr().String()
p.logger.Info("client connected",
    slog.String("remote_addr", remoteAddr),
    slog.String("user", username),
    slog.String("vhost", vhost),
)
start := time.Now()
defer p.logger.Info("client disconnected",
    slog.String("remote_addr", remoteAddr),
    slog.String("user", username),
    slog.String("vhost", vhost),
    slog.Int64("duration_ms", time.Since(start).Milliseconds()),
)
```

In `handleConnection()`, when upstream is unavailable (after the `getOrCreateManagedUpstream` error), add:
```go
p.logger.Warn("client rejected — upstream unavailable",
    slog.String("remote_addr", clientConn.RemoteAddr().String()),
    slog.String("error", err.Error()),
)
```

### Step 4: Run tests to verify they pass

```bash
go test ./proxy/ -run "TestProxyLogsStart|TestClientConnect" -race -v
```

Expected: PASS.

### Step 5: Run full suite

```bash
go test ./... -race
```

Expected: all pass.

### Step 6: Commit

```bash
git add proxy/proxy.go proxy/proxy_test.go
git commit -m "feat(logging): log proxy start/stop and client connect/disconnect"
```

---

## Task 3: Add logger to ManagedUpstream — log upstream lifecycle events

**Goal:** Add `logger *slog.Logger` and `upstreamAddr string` to `ManagedUpstream`. Log: upstream connected, upstream lost, upstream reconnecting, upstream reconnected. Update `handleUpstreamFailure` to accept `cause error` for accurate logging.

**Files:**
- Modify: `proxy/managed_upstream.go`
- Modify: `proxy/proxy.go` (pass logger + addr when constructing ManagedUpstream)
- Modify: `proxy/managed_upstream_test.go`

### Step 1: Write failing tests

Add to `proxy/managed_upstream_test.go`:

```go
func TestManagedUpstreamLogsConnected(t *testing.T) {
    lc, logger := newCapture()

    upstream := makeTestUpstream(t) // existing helper in managed_upstream_test.go
    upstream.logger = logger
    upstream.upstreamAddr = "localhost:5672"

    conn := makeStubUpstreamConn(t) // existing helper
    upstream.Start(conn)
    defer upstream.stopped.Store(true)

    assert.True(t, lc.waitForMessage("upstream connected", 200*time.Millisecond))

    val, ok := lc.attrValue("upstream_addr")
    assert.True(t, ok)
    assert.Equal(t, "localhost:5672", val.String())
}

func TestManagedUpstreamLogsReconnect(t *testing.T) {
    lc, logger := newCapture()

    attempts := 0
    upstream := &ManagedUpstream{
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
    upstream.dialFn = func() (*UpstreamConn, error) {
        attempts++
        if attempts < 2 {
            return nil, errors.New("connection refused")
        }
        return makeStubUpstreamConn(t), nil
    }

    upstream.handleUpstreamFailure(errors.New("read: connection reset by peer"))

    assert.True(t, lc.waitForMessage("upstream lost", 500*time.Millisecond))
    assert.True(t, lc.waitForMessage("upstream reconnected", 500*time.Millisecond))
}
```

### Step 2: Run to verify tests fail

```bash
go test ./proxy/ -run "TestManagedUpstreamLogs" -v
```

Expected: compile errors — `logger`, `upstreamAddr` fields don't exist; `handleUpstreamFailure` wrong arity.

### Step 3: Update `proxy/managed_upstream.go`

Add fields to `ManagedUpstream`:
```go
type ManagedUpstream struct {
    // ... existing fields ...
    upstreamAddr string     // "host:port" — for log fields
    logger       *slog.Logger
}
```

Add `"log/slog"` to imports.

Update `handleUpstreamFailure` signature to accept `cause error`:
```go
func (m *ManagedUpstream) handleUpstreamFailure(cause error) {
```

Update the single callsite in `readLoop`:
```go
m.handleUpstreamFailure(err)
```

Add log calls:

In `Start()`, after `go m.readLoop()`:
```go
if m.logger != nil {
    m.logger.Info("upstream connected",
        slog.String("upstream_addr", m.upstreamAddr),
        slog.String("user", m.username),
        slog.String("vhost", m.vhost),
    )
}
```

In `handleUpstreamFailure()`, at the top (before aborting clients):
```go
if m.logger != nil {
    errStr := ""
    if cause != nil {
        errStr = cause.Error()
    }
    m.logger.Warn("upstream lost",
        slog.String("upstream_addr", m.upstreamAddr),
        slog.String("user", m.username),
        slog.String("vhost", m.vhost),
        slog.String("error", errStr),
    )
}
```

In `reconnectLoop()`, before `time.Sleep(wait)`:
```go
if m.logger != nil {
    m.logger.Warn("upstream reconnecting",
        slog.String("upstream_addr", m.upstreamAddr),
        slog.String("user", m.username),
        slog.String("vhost", m.vhost),
        slog.Int("attempt", attempt),
        slog.Int64("backoff_ms", wait.Milliseconds()),
    )
}
```

(Add `attempt int` counter that increments each loop iteration, starting at 1.)

After `go m.readLoop()` in `reconnectLoop()`:
```go
if m.logger != nil {
    m.logger.Info("upstream reconnected",
        slog.String("upstream_addr", m.upstreamAddr),
        slog.String("user", m.username),
        slog.String("vhost", m.vhost),
        slog.Int("attempt", attempt),
    )
}
```

### Step 4: Update `proxy/proxy.go` — pass logger + addr to ManagedUpstream

In `getOrCreateManagedUpstream`, when constructing `m`:
```go
m := &ManagedUpstream{
    // ... existing fields ...
    upstreamAddr: addr,
    logger:       p.logger,
}
```

### Step 5: Run tests

```bash
go test ./proxy/ -run "TestManagedUpstreamLogs" -race -v
```

Expected: PASS.

### Step 6: Run full suite

```bash
go test ./... -race
```

Expected: all pass.

### Step 7: Commit

```bash
git add proxy/managed_upstream.go proxy/proxy.go proxy/managed_upstream_test.go
git commit -m "feat(logging): log upstream connected/lost/reconnecting/reconnected"
```

---

## Task 4: Debug-level channel logs + full race-detector verification

**Goal:** Add DEBUG-level logs for channel allocation and release. Run the full suite under `-race` and verify clean output.

**Files:**
- Modify: `proxy/managed_upstream.go`
- Modify: `proxy/managed_upstream_test.go`

### Step 1: Write failing test

Add to `proxy/managed_upstream_test.go`:

```go
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

    client := &stubClient{delivered: make(chan struct{}, 10)}
    upstreamID, err := m.AllocateChannel(1, client)
    require.NoError(t, err)

    msgs := lc.messages()
    assert.Contains(t, msgs, "channel allocated")

    m.ReleaseChannel(upstreamID)
    assert.Contains(t, lc.messages(), "channel released")
}
```

### Step 2: Run to verify it fails

```bash
go test ./proxy/ -run TestAllocateReleaseChannelLogged -v
```

Expected: FAIL — "channel allocated" not in messages.

### Step 3: Add debug log calls to `managed_upstream.go`

In `AllocateChannel`, after `return id, nil`:
```go
// (log before return)
if m.logger != nil {
    m.logger.Debug("channel allocated",
        slog.Int("client_chan", int(clientChanID)),
        slog.Int("upstream_chan", int(id)),
        slog.String("user", m.username),
        slog.String("vhost", m.vhost),
    )
}
return id, nil
```

In `ReleaseChannel`:
```go
func (m *ManagedUpstream) ReleaseChannel(upstreamChanID uint16) {
    m.mu.Lock()
    defer m.mu.Unlock()
    delete(m.usedChannels, upstreamChanID)
    delete(m.channelOwners, upstreamChanID)
    if m.logger != nil {
        m.logger.Debug("channel released",
            slog.Int("upstream_chan", int(upstreamChanID)),
            slog.String("user", m.username),
            slog.String("vhost", m.vhost),
        )
    }
}
```

### Step 4: Run test to verify it passes

```bash
go test ./proxy/ -run TestAllocateReleaseChannelLogged -v
```

Expected: PASS.

### Step 5: Run full suite with race detector

```bash
go test ./... -race -count=1
```

Expected: all tests pass, no data race output.

### Step 6: Commit

```bash
git add proxy/managed_upstream.go proxy/managed_upstream_test.go
git commit -m "feat(logging): add DEBUG channel allocation/release logs; full race-clean run"
```

---

## Done

After Task 4, structured logging is complete. Run `go test ./... -race` one final time to confirm a clean baseline, then use `superpowers:finishing-a-development-branch` to merge or create a PR.
