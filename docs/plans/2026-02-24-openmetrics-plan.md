# OpenMetrics Endpoint Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Expose a `/metrics` endpoint in Prometheus text format on the existing health port so the New Relic Infrastructure agent can scrape proxy health and load metrics.

**Architecture:** A `MetricsHandler()` method on `Proxy` reads current state under a read lock and writes Prometheus text format directly to the HTTP response — no library, no background collection. It is wired into the existing health server via `http.ServeMux`. One new `atomic.Int64` field (`reconnectTotal`) on `ManagedUpstream` tracks cumulative reconnect attempts.

**Tech Stack:** Go standard library only (`net/http`, `fmt`). No new dependencies.

---

## Context: Key files

- `proxy/managed_upstream.go` — `ManagedUpstream` struct and `reconnectLoop`
- `proxy/proxy.go` — `Proxy` struct; `mu sync.RWMutex` guards `upstreams` map
- `proxy/managed_upstream_test.go` — unit test helpers: `newTestManagedUpstream`, `stubClient`
- `proxy/test_helpers_test.go` — `discardLogger()`
- `main.go` — health server goroutine (lines 58–64)

---

## Task 1: Add `reconnectTotal` counter to `ManagedUpstream`

**Files:**
- Modify: `proxy/managed_upstream.go`
- Modify: `proxy/managed_upstream_test.go`

### Step 1: Write the failing test

Add to `proxy/managed_upstream_test.go`:

```go
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
```

### Step 2: Run to verify it fails

```bash
go test ./proxy/ -run TestReconnectTotalIncrements -v
```

Expected: FAIL — `m.reconnectTotal` field doesn't exist yet.

### Step 3: Add the field to `ManagedUpstream`

In `proxy/managed_upstream.go`, add after the `stopped atomic.Bool` field (around line 44):

```go
reconnectTotal atomic.Int64 // cumulative reconnect attempts since start
```

### Step 4: Increment it in `reconnectLoop`

In `proxy/managed_upstream.go`, in `reconnectLoop`, add `m.reconnectTotal.Add(1)` immediately after `attempt++` (around line 306):

```go
attempt++
m.reconnectTotal.Add(1)
```

### Step 5: Run to verify it passes

```bash
go test ./proxy/ -run TestReconnectTotalIncrements -v
```

Expected: PASS

```bash
go test ./... -race
```

Expected: all pass, no races.

### Step 6: Commit

```bash
git add proxy/managed_upstream.go proxy/managed_upstream_test.go
git commit -m "feat(proxy): add reconnectTotal atomic counter to ManagedUpstream"
```

---

## Task 2: Implement `MetricsHandler` on `Proxy`

**Files:**
- Create: `proxy/metrics.go`
- Create: `proxy/metrics_test.go`

### Step 1: Write the failing test

Create `proxy/metrics_test.go`:

```go
package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timsweb/amqplex/config"
)

func TestMetricsHandler(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:  "localhost",
		ListenPort:     15740,
		UpstreamURL:    "amqp://localhost:5672",
		PoolMaxChannels: 65535,
	}
	p, err := NewProxy(cfg, discardLogger())
	require.NoError(t, err)

	// Set known state.
	p.activeClients.Store(3)

	key := p.getPoolKey("guest", "guest", "/")

	// Upstream 1: connected (conn != nil), 2 used channels, 1 pending close, 5 reconnect attempts.
	m1 := newTestManagedUpstream(65535)
	m1.conn = &UpstreamConn{} // non-nil = connected
	m1.usedChannels[1] = true
	m1.usedChannels[2] = true
	m1.pendingClose[3] = true
	m1.reconnectTotal.Store(5)

	// Upstream 2: reconnecting (conn == nil by default), 2 reconnect attempts.
	m2 := newTestManagedUpstream(65535)
	m2.reconnectTotal.Store(2)

	p.mu.Lock()
	p.upstreams[key] = []*ManagedUpstream{m1, m2}
	p.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	p.MetricsHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/plain")

	body := rec.Body.String()
	assert.Contains(t, body, "\namqproxy_active_clients 3\n")
	assert.Contains(t, body, "\namqproxy_upstream_connections 2\n")
	assert.Contains(t, body, "\namqproxy_upstream_reconnecting 1\n")
	assert.Contains(t, body, "\namqproxy_channels_used 2\n")
	assert.Contains(t, body, "\namqproxy_channels_pending_close 1\n")
	assert.Contains(t, body, "\namqproxy_upstream_reconnect_attempts_total 7\n")
}
```

### Step 2: Run to verify it fails

```bash
go test ./proxy/ -run TestMetricsHandler -v
```

Expected: FAIL — `MetricsHandler` method doesn't exist yet.

### Step 3: Create `proxy/metrics.go`

```go
package proxy

import (
	"fmt"
	"net/http"
)

// MetricsHandler returns an http.Handler that serves proxy metrics in Prometheus
// text format (compatible with OpenMetrics and the New Relic Infrastructure
// agent's Prometheus integration).
//
// Metrics are collected on each scrape under a read lock — no background
// goroutine, no caching. At typical scrape intervals (15–60s) the contention
// is negligible.
func (p *Proxy) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		activeClients := int(p.activeClients.Load())

		var (
			upstreamTotal        int
			upstreamReconnecting int
			channelsUsed         int
			channelsPendingClose int
			reconnectAttempts    int64
		)

		// Collect upstream metrics under p.mu (read) then m.mu (write) —
		// consistent with the p.mu → m.mu ordering used elsewhere.
		p.mu.RLock()
		for _, upstreams := range p.upstreams {
			for _, m := range upstreams {
				upstreamTotal++
				m.mu.Lock()
				if m.conn == nil {
					upstreamReconnecting++
				}
				channelsUsed += len(m.usedChannels)
				channelsPendingClose += len(m.pendingClose)
				m.mu.Unlock()
				reconnectAttempts += m.reconnectTotal.Load()
			}
		}
		p.mu.RUnlock()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		fmt.Fprintf(w, "# HELP amqproxy_active_clients Current number of connected clients.\n")
		fmt.Fprintf(w, "# TYPE amqproxy_active_clients gauge\n")
		fmt.Fprintf(w, "amqproxy_active_clients %d\n", activeClients)

		fmt.Fprintf(w, "# HELP amqproxy_upstream_connections Total upstream AMQP connections.\n")
		fmt.Fprintf(w, "# TYPE amqproxy_upstream_connections gauge\n")
		fmt.Fprintf(w, "amqproxy_upstream_connections %d\n", upstreamTotal)

		fmt.Fprintf(w, "# HELP amqproxy_upstream_reconnecting Upstream connections currently in reconnect loop.\n")
		fmt.Fprintf(w, "# TYPE amqproxy_upstream_reconnecting gauge\n")
		fmt.Fprintf(w, "amqproxy_upstream_reconnecting %d\n", upstreamReconnecting)

		fmt.Fprintf(w, "# HELP amqproxy_channels_used Total AMQP channels currently allocated.\n")
		fmt.Fprintf(w, "# TYPE amqproxy_channels_used gauge\n")
		fmt.Fprintf(w, "amqproxy_channels_used %d\n", channelsUsed)

		fmt.Fprintf(w, "# HELP amqproxy_channels_pending_close Channels awaiting Channel.CloseOk from broker.\n")
		fmt.Fprintf(w, "# TYPE amqproxy_channels_pending_close gauge\n")
		fmt.Fprintf(w, "amqproxy_channels_pending_close %d\n", channelsPendingClose)

		fmt.Fprintf(w, "# HELP amqproxy_upstream_reconnect_attempts_total Cumulative upstream reconnect attempts since proxy start.\n")
		fmt.Fprintf(w, "# TYPE amqproxy_upstream_reconnect_attempts_total counter\n")
		fmt.Fprintf(w, "amqproxy_upstream_reconnect_attempts_total %d\n", reconnectAttempts)
	})
}
```

### Step 4: Run to verify it passes

```bash
go test ./proxy/ -run TestMetricsHandler -v
```

Expected: PASS

```bash
go test ./... -race
```

Expected: all pass, no races.

### Step 5: Commit

```bash
git add proxy/metrics.go proxy/metrics_test.go
git commit -m "feat(proxy): add MetricsHandler serving Prometheus text format"
```

---

## Task 3: Wire `/metrics` into the health server in `main.go`

**Files:**
- Modify: `main.go`

No new imports needed — `net/http` is already imported.

### Step 1: Replace the health server goroutine

In `main.go`, replace lines 58–64:

**Before:**
```go
// Start health check server
go func() {
	healthAddr := fmt.Sprintf("%s:%d", cfg.ListenAddress, cfg.ListenPort+1)
	healthHandler := health.NewHealthHandler()
	log.Printf("Health check server listening on %s", healthAddr)
	log.Fatal(http.ListenAndServe(healthAddr, healthHandler))
}()
```

**After:**
```go
// Start health/metrics server
go func() {
	addr := fmt.Sprintf("%s:%d", cfg.ListenAddress, cfg.ListenPort+1)
	mux := http.NewServeMux()
	mux.Handle("/", health.NewHealthHandler())
	mux.Handle("/metrics", p.MetricsHandler())
	log.Printf("Health/metrics server listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}()
```

### Step 2: Build to verify it compiles

```bash
go build ./...
```

Expected: no errors.

### Step 3: Run all tests

```bash
go test ./... -race
```

Expected: all pass, no races.

### Step 4: Commit

```bash
git add main.go
git commit -m "feat: wire /metrics endpoint into health server"
```

---

## Verification

```bash
# All unit tests with race detector
go test ./... -race -count=1

# Manual smoke test (requires a running proxy)
curl -s http://localhost:5674/metrics
# Expected output includes lines like:
# amqproxy_active_clients 0
# amqproxy_upstream_connections 0
# amqproxy_upstream_reconnect_attempts_total 0
```
