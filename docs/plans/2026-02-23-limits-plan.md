# Limits Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a global upstream connection cap and a concurrent client connection cap, both configurable and defaulting to 0 (unlimited).

**Architecture:** Two new `Config` fields drive two enforcement points: client limit checked atomically in the `Start()` accept loop before spawning a goroutine; upstream limit counted inside `getOrCreateManagedUpstream` under the existing write lock before dialing. Both reject with existing error paths (bare TCP close for clients, 503 `Connection.Close` for upstream limit).

**Tech Stack:** Go standard library — `sync/atomic` (already in use), `config` package, `proxy` package.

---

## Task 1: Add config fields

### Files
- Modify: `config/config.go`

### Step 1: Add the two fields to the `Config` struct

In `config/config.go`, the `Config` struct currently ends at `TLSSkipVerify bool`. Add after `PoolMaxChannels`:

```go
type Config struct {
	ListenAddress          string
	ListenPort             int
	PoolIdleTimeout        int
	PoolMaxChannels        int
	MaxUpstreamConnections int // 0 = unlimited
	MaxClientConnections   int // 0 = unlimited
	UpstreamURL            string
	// ... TLS fields unchanged
```

### Step 2: Add defaults

In `LoadConfig`, after the existing `v.SetDefault("pool.max_channels", 65535)` line, add:

```go
v.SetDefault("pool.max_upstream_connections", 0)
v.SetDefault("pool.max_client_connections", 0)
```

### Step 3: Wire up the populated Config struct

In the `cfg := &Config{...}` block, after `PoolMaxChannels: v.GetInt("pool.max_channels"),`, add:

```go
MaxUpstreamConnections: v.GetInt("pool.max_upstream_connections"),
MaxClientConnections:   v.GetInt("pool.max_client_connections"),
```

### Step 4: Build to confirm no errors

```bash
go build ./...
```

Expected: clean build.

### Step 5: Commit

```bash
git add config/config.go
git commit -m "feat(config): add MaxUpstreamConnections and MaxClientConnections fields"
```

---

## Task 2: Client connection limit (TDD)

### Files
- Modify: `proxy/proxy.go`
- Modify: `proxy/proxy_test.go`

### Step 1: Write the failing tests

Add to `proxy/proxy_test.go`:

```go
func TestClientLimitRejectsOverLimit(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:        "localhost",
		ListenPort:           15710,
		UpstreamURL:          "amqp://localhost:5672",
		MaxClientConnections: 1,
		PoolMaxChannels:      65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()
	time.Sleep(10 * time.Millisecond)

	// Simulate one active client already occupying the single slot.
	p.activeClients.Add(1)
	defer p.activeClients.Add(-1)

	conn, err := net.Dial("tcp", "localhost:15710")
	require.NoError(t, err)
	defer conn.Close()

	// Proxy should close the connection immediately — any read returns EOF or error.
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	assert.Error(t, err, "over-limit connection should be closed immediately")
}

func TestClientLimitAllowsUnderLimit(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:        "localhost",
		ListenPort:           15711,
		UpstreamURL:          "amqp://localhost:5672",
		MaxClientConnections: 2,
		PoolMaxChannels:      65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()
	time.Sleep(10 * time.Millisecond)

	// One active client — still one slot free.
	p.activeClients.Add(1)
	defer p.activeClients.Add(-1)

	conn, err := net.Dial("tcp", "localhost:15711")
	require.NoError(t, err)
	defer conn.Close()

	// Proxy should accept the connection and begin the AMQP handshake.
	_, err = conn.Write([]byte(ProtocolHeader))
	require.NoError(t, err)
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	n, _ := conn.Read(buf)
	assert.Greater(t, n, 0, "proxy should respond when under limit")
}

func TestClientLimitZeroMeansUnlimited(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:        "localhost",
		ListenPort:           15712,
		UpstreamURL:          "amqp://localhost:5672",
		MaxClientConnections: 0, // unlimited
		PoolMaxChannels:      65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	go p.Start()
	defer p.Stop()
	time.Sleep(10 * time.Millisecond)

	// Crank the counter to a large value — with limit=0 it must be ignored.
	p.activeClients.Add(1000)
	defer p.activeClients.Add(-1000)

	conn, err := net.Dial("tcp", "localhost:15712")
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Write([]byte(ProtocolHeader))
	require.NoError(t, err)
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	n, _ := conn.Read(buf)
	assert.Greater(t, n, 0, "proxy should respond when limit is 0")
}
```

### Step 2: Run to confirm they fail

```bash
go test ./proxy/ -run "TestClientLimit" -v 2>&1 | head -20
```

Expected: build failure — `p.activeClients` undefined.

### Step 3: Add `activeClients` field to `Proxy` struct

In `proxy/proxy.go`, the `Proxy` struct is:

```go
type Proxy struct {
	listener    *AMQPListener
	config      *config.Config
	upstreams   map[[32]byte][]*ManagedUpstream
	netListener net.Listener
	mu          sync.RWMutex
	wg          sync.WaitGroup
	logger      *slog.Logger
}
```

Add `activeClients` between `wg` and `logger`:

```go
type Proxy struct {
	listener    *AMQPListener
	config      *config.Config
	upstreams   map[[32]byte][]*ManagedUpstream
	netListener net.Listener
	mu          sync.RWMutex
	wg          sync.WaitGroup
	activeClients atomic.Int32
	logger      *slog.Logger
}
```

`atomic` is already imported (`sync/atomic` is used by `ManagedUpstream`). `atomic.Int32` is a struct value so no initialisation is required.

### Step 4: Enforce the limit in `Start()`

The current accept loop in `proxy/proxy.go` (lines 87–98):

```go
	p.mu.RLock()
	if p.netListener == nil {
		p.mu.RUnlock()
		conn.Close()
		return nil
	}
	p.wg.Add(1)
	p.mu.RUnlock()
	go func() {
		defer p.wg.Done()
		p.handleConnection(conn)
	}()
```

Replace with:

```go
	p.mu.RLock()
	if p.netListener == nil {
		p.mu.RUnlock()
		conn.Close()
		return nil
	}
	if p.config.MaxClientConnections > 0 &&
		int(p.activeClients.Load()) >= p.config.MaxClientConnections {
		p.mu.RUnlock()
		conn.Close()
		continue
	}
	p.wg.Add(1)
	p.activeClients.Add(1)
	p.mu.RUnlock()
	go func() {
		defer p.wg.Done()
		p.handleConnection(conn)
	}()
```

The check is performed under `p.mu.RLock()` — the same lock that guards `wg.Add` — so it is sequentially consistent with `Stop()`. `Start()` is a single goroutine, so the check-then-increment is not subject to TOCTOU races from within the loop.

### Step 5: Decrement in `handleConnection`

In `proxy/proxy.go`, `handleConnection` starts at line 208:

```go
func (p *Proxy) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()
```

Add the counter decrement immediately after `defer clientConn.Close()`:

```go
func (p *Proxy) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()
	defer p.activeClients.Add(-1)
```

### Step 6: Run the new tests

```bash
go test ./proxy/ -run "TestClientLimit" -race -v
```

Expected: all three PASS.

### Step 7: Run full suite

```bash
go test ./... -race -count=1
```

Expected: all packages pass.

### Step 8: Commit

```bash
git add proxy/proxy.go proxy/proxy_test.go
git commit -m "feat(proxy): enforce MaxClientConnections limit in accept loop"
```

---

## Task 3: Upstream connection limit (TDD)

### Files
- Modify: `proxy/proxy.go`
- Modify: `proxy/proxy_test.go`

### Step 1: Write the failing tests

Add to `proxy/proxy_test.go`:

```go
func TestUpstreamLimitRejectsNewDial(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:          "localhost",
		ListenPort:             15713,
		UpstreamURL:            "amqp://localhost:5672",
		MaxUpstreamConnections: 1,
		PoolMaxChannels:        65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	// Pre-populate one upstream — exactly at the limit.
	key := p.getPoolKey("user", "pass", "/")
	m := newTestManagedUpstream(65535)
	p.mu.Lock()
	p.upstreams[key] = []*ManagedUpstream{m}
	p.mu.Unlock()

	// Requesting a different credential set should fail with the limit error,
	// not a dial error.
	_, err = p.getOrCreateManagedUpstream("other", "pass", "/other")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream connection limit reached")
}

func TestUpstreamLimitAllowsUnderLimit(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:          "localhost",
		ListenPort:             15714,
		UpstreamURL:            "amqp://localhost:19999", // nothing listening
		MaxUpstreamConnections: 2,
		PoolMaxChannels:        65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	// One upstream in the map — still one slot free.
	key := p.getPoolKey("user", "pass", "/")
	m := newTestManagedUpstream(65535)
	p.mu.Lock()
	p.upstreams[key] = []*ManagedUpstream{m}
	p.mu.Unlock()

	_, err = p.getOrCreateManagedUpstream("other", "pass", "/other")
	// Must fail with a dial error (nothing listening), NOT a limit error.
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "connection limit reached")
}

func TestUpstreamLimitZeroMeansUnlimited(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:          "localhost",
		ListenPort:             15715,
		UpstreamURL:            "amqp://localhost:19999",
		MaxUpstreamConnections: 0, // unlimited
		PoolMaxChannels:        65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	// Pre-populate many upstreams — with limit=0 none of this should matter.
	p.mu.Lock()
	for i := 0; i < 100; i++ {
		key := p.getPoolKey(fmt.Sprintf("user%d", i), "pass", "/")
		p.upstreams[key] = []*ManagedUpstream{newTestManagedUpstream(65535)}
	}
	p.mu.Unlock()

	_, err = p.getOrCreateManagedUpstream("newuser", "pass", "/")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "connection limit reached")
}
```

`fmt` is already imported in `proxy_test.go`? Check — if not, add it. The test file currently imports `bufio`, `net`, `testing`, `time`, and the testify/assert packages. Add `"fmt"` to the import block.

### Step 2: Run to confirm they fail

```bash
go test ./proxy/ -run "TestUpstreamLimit" -v 2>&1 | head -20
```

Expected: build failure — `fmt` undefined (if import is missing) or test failures once it compiles.

### Step 3: Add `"fmt"` to `proxy_test.go` imports if missing

The import block in `proxy/proxy_test.go` currently:

```go
import (
	"bufio"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timsweb/amqplex/config"
)
```

Add `"fmt"`:

```go
import (
	"bufio"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timsweb/amqplex/config"
)
```

### Step 4: Enforce the limit in `getOrCreateManagedUpstream`

In `proxy/proxy.go`, the section after the re-check under write lock (line 133):

```go
	// All existing upstreams full (or none exist): dial a new one
	network, addr, err := parseUpstreamURL(p.config.UpstreamURL)
```

Insert the limit check immediately before the dial:

```go
	// All existing upstreams full (or none exist): check global cap before dialling.
	if p.config.MaxUpstreamConnections > 0 {
		total := 0
		for _, slice := range p.upstreams {
			total += len(slice)
		}
		if total >= p.config.MaxUpstreamConnections {
			return nil, errors.New("upstream connection limit reached")
		}
	}

	network, addr, err := parseUpstreamURL(p.config.UpstreamURL)
```

`errors` is already imported in `proxy/proxy.go`.

### Step 5: Run the new tests

```bash
go test ./proxy/ -run "TestUpstreamLimit" -race -v
```

Expected: all three PASS.

### Step 6: Run full suite

```bash
go test ./... -race -count=1
```

Expected: all packages pass.

### Step 7: Commit

```bash
git add proxy/proxy.go proxy/proxy_test.go
git commit -m "feat(proxy): enforce MaxUpstreamConnections limit before dialling"
```

---

## Verification

```bash
go test ./proxy/ -run "TestClientLimit|TestUpstreamLimit" -race -v
go test ./... -race -count=1
```

All 6 new tests and all existing tests must pass race-clean.
