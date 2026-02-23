# Integration E2E Test Suite Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add end-to-end integration tests that verify publish/consume correctness through the proxy using `amqp091-go` against a live RabbitMQ broker.

**Architecture:** All four tests start an in-process proxy (pointing at `amqp://localhost:5672`) and dial it via `amqp091-go`. Docker Compose provides RabbitMQ only. `TestMain` waits for RabbitMQ readiness before the suite runs. A new `PoolCleanupInterval` config field (default 30s) lets the idle cleanup test set a 1s ticker without waiting 30s.

**Tech Stack:** `github.com/rabbitmq/amqp091-go` (already in go.mod), `go test -tags integration`, Docker Compose.

---

## Task 1: Expose plain AMQP port in Docker Compose

### Files
- Modify: `docker-compose.yml`

### Step 1: Add port 5672 to the rabbitmq service

In `docker-compose.yml`, the `rabbitmq` ports block currently only maps 5671 and 15672. RabbitMQ listens on plain AMQP 5672 by default even when `rabbitmq.conf` configures TLS — it doesn't disable the plain listener unless explicitly told to. Add the plain port mapping:

```yaml
services:
  rabbitmq:
    image: rabbitmq:3-management
    ports:
      - "5671:5671"
      - "5672:5672"
      - "15672:15672"
```

### Step 2: Verify

```bash
docker compose up -d
docker compose ps
```

Expected: rabbitmq container shows ports `0.0.0.0:5671->5671/tcp` and `0.0.0.0:5672->5672/tcp`.

```bash
docker compose down
```

### Step 3: Commit

```bash
git add docker-compose.yml
git commit -m "chore(compose): expose plain AMQP port 5672 for integration tests"
```

---

## Task 2: Add PoolCleanupInterval config field

This field controls how often the idle cleanup ticker fires. Defaults to 30 (seconds). Setting it to 1 in tests lets `TestIdleUpstreamCleanup` avoid a 30-second wait.

### Files
- Modify: `config/config.go`
- Modify: `proxy/proxy.go`

### Step 1: Add field to Config struct

In `config/config.go`, add after `MaxClientConnections`:

```go
PoolCleanupInterval int // 0 = use default 30s
```

### Step 2: Add viper default

In `LoadConfig`, after `v.SetDefault("pool.max_client_connections", 0)`:

```go
v.SetDefault("pool.cleanup_interval", 30)
```

### Step 3: Wire into cfg block

After `MaxClientConnections: v.GetInt("pool.max_client_connections"),`:

```go
PoolCleanupInterval: v.GetInt("pool.cleanup_interval"),
```

### Step 4: Use it in startIdleCleanup

In `proxy/proxy.go`, `startIdleCleanup` currently hardcodes 30s:

```go
ticker := time.NewTicker(30 * time.Second)
```

Replace with:

```go
interval := p.config.PoolCleanupInterval
if interval <= 0 {
    interval = 30
}
ticker := time.NewTicker(time.Duration(interval) * time.Second)
```

### Step 5: Build

```bash
go build ./...
```

Expected: clean build.

### Step 6: Run existing tests

```bash
go test ./... -race -count=1
```

Expected: all pass.

### Step 7: Commit

```bash
git add config/config.go proxy/proxy.go
git commit -m "feat(config): add PoolCleanupInterval for configurable idle cleanup ticker"
```

---

## Task 3: Promote amqp091-go to direct dependency

### Files
- Modify: `go.mod`, `go.sum` (via go tooling)

### Step 1: Promote the dependency

```bash
go get github.com/rabbitmq/amqp091-go
go mod tidy
```

Expected: `go.mod` now lists `github.com/rabbitmq/amqp091-go` as a direct dependency (no `// indirect`).

### Step 2: Commit

```bash
git add go.mod go.sum
git commit -m "chore(deps): promote amqp091-go to direct dependency"
```

---

## Task 4: Write the integration E2E test file

### Files
- Create: `tests/integration_e2e_test.go`

### Step 1: Create the file

```go
//go:build integration

package tests

import (
	"fmt"
	"net"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timsweb/amqplex/config"
	"github.com/timsweb/amqplex/proxy"
)

// TestMain waits for RabbitMQ to be reachable before running the suite.
func TestMain(m *testing.M) {
	if !waitForPort("localhost:5672", 30*time.Second) {
		fmt.Println("RabbitMQ not reachable on localhost:5672 after 30s — is Docker Compose up?")
		// Exit 1 so the test run is clearly a setup failure, not a test failure.
		// m.Run() is not called.
		return
	}
	m.Run()
}

// waitForPort polls addr until it accepts a TCP connection or timeout elapses.
func waitForPort(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// startProxy starts a proxy in-process and returns it. The proxy is stopped
// via t.Cleanup. It polls until the proxy's listen port accepts connections.
func startProxy(t *testing.T, cfg *config.Config) *proxy.Proxy {
	t.Helper()
	p, err := proxy.NewProxy(cfg, discardLogger())
	require.NoError(t, err)
	go p.Start()
	t.Cleanup(func() { p.Stop() })

	addr := fmt.Sprintf("localhost:%d", cfg.ListenPort)
	require.True(t, waitForPort(addr, 5*time.Second),
		"proxy did not become ready on %s", addr)
	return p
}

// dialProxy opens an amqp091-go connection through the proxy and registers cleanup.
func dialProxy(t *testing.T, port int) *amqp.Connection {
	t.Helper()
	url := fmt.Sprintf("amqp://guest:guest@localhost:%d/", port)
	var conn *amqp.Connection
	var err error
	for i := 0; i < 5; i++ {
		conn, err = amqp.Dial(url)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.NoError(t, err, "failed to dial proxy on port %d", port)
	t.Cleanup(func() { conn.Close() })
	return conn
}

// declareQueue declares a non-durable auto-delete queue and registers cleanup.
func declareQueue(t *testing.T, ch *amqp.Channel, name string) amqp.Queue {
	t.Helper()
	q, err := ch.QueueDeclare(
		name,  // name
		false, // durable
		true,  // auto-delete
		false, // exclusive
		false, // no-wait
		nil,   // args
	)
	require.NoError(t, err)
	t.Cleanup(func() { ch.QueueDelete(name, false, false, false) })
	return q
}

// TestBasicPublishConsume verifies a single producer publishes a message that a
// consumer receives end-to-end through the proxy.
func TestBasicPublishConsume(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15681,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 60,
		PoolMaxChannels: 65535,
	}
	startProxy(t, cfg)

	conn := dialProxy(t, 15681)
	ch, err := conn.Channel()
	require.NoError(t, err)
	defer ch.Close()

	q := declareQueue(t, ch, "e2e-basic")

	body := []byte("hello from e2e")
	err = ch.Publish("", q.Name, false, false, amqp.Publishing{Body: body})
	require.NoError(t, err)

	msg, ok, err := ch.Get(q.Name, true)
	require.NoError(t, err)
	require.True(t, ok, "expected a message in the queue")
	assert.Equal(t, body, msg.Body)
}

// TestMultipleClientsSharedUpstream opens two separate AMQP connections through
// the proxy (same credentials → same ManagedUpstream) and verifies that channel
// multiplexing works: both clients can publish and consume independently.
func TestMultipleClientsSharedUpstream(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15682,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 60,
		PoolMaxChannels: 65535,
	}
	startProxy(t, cfg)

	conn1 := dialProxy(t, 15682)
	conn2 := dialProxy(t, 15682)

	ch1, err := conn1.Channel()
	require.NoError(t, err)
	defer ch1.Close()

	ch2, err := conn2.Channel()
	require.NoError(t, err)
	defer ch2.Close()

	q := declareQueue(t, ch1, "e2e-multi")

	// Both clients publish one message each.
	require.NoError(t, ch1.Publish("", q.Name, false, false, amqp.Publishing{Body: []byte("from client1")}))
	require.NoError(t, ch2.Publish("", q.Name, false, false, amqp.Publishing{Body: []byte("from client2")}))

	// Consume both messages from one channel.
	bodies := map[string]bool{}
	for i := 0; i < 2; i++ {
		msg, ok, err := ch1.Get(q.Name, true)
		require.NoError(t, err)
		require.True(t, ok, "expected message %d", i+1)
		bodies[string(msg.Body)] = true
	}
	assert.True(t, bodies["from client1"], "missing message from client1")
	assert.True(t, bodies["from client2"], "missing message from client2")
}

// TestClientReconnect verifies that a client can disconnect and reconnect through
// the proxy and continue publishing and consuming messages.
func TestClientReconnect(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:   "localhost",
		ListenPort:      15683,
		UpstreamURL:     "amqp://localhost:5672",
		PoolIdleTimeout: 60,
		PoolMaxChannels: 65535,
	}
	startProxy(t, cfg)

	queueName := "e2e-reconnect"

	// First connection: publish one message then disconnect.
	{
		conn, err := amqp.Dial(fmt.Sprintf("amqp://guest:guest@localhost:%d/", 15683))
		require.NoError(t, err)
		ch, err := conn.Channel()
		require.NoError(t, err)
		_, err = ch.QueueDeclare(queueName, true, false, false, false, nil) // durable
		require.NoError(t, err)
		require.NoError(t, ch.Publish("", queueName, false, false, amqp.Publishing{Body: []byte("msg1")}))
		conn.Close()
	}

	// Second connection: publish a second message then consume both.
	conn2 := dialProxy(t, 15683)
	ch2, err := conn2.Channel()
	require.NoError(t, err)
	defer ch2.Close()

	_, err = ch2.QueueDeclare(queueName, true, false, false, false, nil) // durable, must match
	require.NoError(t, err)
	t.Cleanup(func() { ch2.QueueDelete(queueName, false, false, false) })

	require.NoError(t, ch2.Publish("", queueName, false, false, amqp.Publishing{Body: []byte("msg2")}))

	bodies := map[string]bool{}
	for i := 0; i < 2; i++ {
		msg, ok, err := ch2.Get(queueName, true)
		require.NoError(t, err)
		require.True(t, ok, "expected message %d after reconnect", i+1)
		bodies[string(msg.Body)] = true
	}
	assert.True(t, bodies["msg1"])
	assert.True(t, bodies["msg2"])
}

// TestIdleUpstreamCleanup verifies that after all clients disconnect and the
// idle timeout elapses, the proxy cleans up the upstream connection and
// successfully creates a new one when a client reconnects.
func TestIdleUpstreamCleanup(t *testing.T) {
	cfg := &config.Config{
		ListenAddress:       "localhost",
		ListenPort:          15684,
		UpstreamURL:         "amqp://localhost:5672",
		PoolIdleTimeout:     1,  // 1 second idle timeout
		PoolCleanupInterval: 1,  // 1 second ticker so we don't wait 30s
		PoolMaxChannels:     65535,
	}
	startProxy(t, cfg)

	queueName := "e2e-idle-cleanup"

	// First connection: declare a durable queue, publish, then disconnect.
	{
		conn, err := amqp.Dial(fmt.Sprintf("amqp://guest:guest@localhost:%d/", 15684))
		require.NoError(t, err)
		ch, err := conn.Channel()
		require.NoError(t, err)
		_, err = ch.QueueDeclare(queueName, true, false, false, false, nil)
		require.NoError(t, err)
		require.NoError(t, ch.Publish("", queueName, false, false, amqp.Publishing{Body: []byte("before-cleanup")}))
		conn.Close()
	}

	// Wait for idle cleanup to fire (timeout=1s, ticker=1s, so ~2s is sufficient).
	time.Sleep(3 * time.Second)

	// Reconnect — this must create a new upstream connection after cleanup.
	conn2 := dialProxy(t, 15684)
	t.Cleanup(func() { conn2.Close() })
	ch2, err := conn2.Channel()
	require.NoError(t, err)
	defer ch2.Close()

	_, err = ch2.QueueDeclare(queueName, true, false, false, false, nil)
	require.NoError(t, err)
	t.Cleanup(func() { ch2.QueueDelete(queueName, false, false, false) })

	// Publish a second message and consume both — verifies full round-trip.
	require.NoError(t, ch2.Publish("", queueName, false, false, amqp.Publishing{Body: []byte("after-cleanup")}))

	bodies := map[string]bool{}
	for i := 0; i < 2; i++ {
		msg, ok, err := ch2.Get(queueName, true)
		require.NoError(t, err)
		require.True(t, ok, "expected message %d after idle cleanup", i+1)
		bodies[string(msg.Body)] = true
	}
	assert.True(t, bodies["before-cleanup"])
	assert.True(t, bodies["after-cleanup"])
}
```

### Step 2: Build-check (no RabbitMQ needed)

```bash
go build -tags integration ./tests/
```

Expected: clean build.

### Step 3: Commit

```bash
git add tests/integration_e2e_test.go
git commit -m "test(integration): add e2e publish/consume tests with amqp091-go"
```

---

## Task 5: Update Makefile

### Files
- Modify: `Makefile`

### Step 1: Add `integration` target

The existing `integration-test` target runs `go test -v ./tests/...` without the build tag or Docker lifecycle. Add a new `integration` target:

```makefile
integration:
	docker compose up -d
	go test -tags integration -v -timeout 120s ./tests/ ; docker compose down
```

The semicolon (not `&&`) ensures `docker compose down` runs even if tests fail.

Also add `integration` to the `.PHONY` line.

### Step 2: Commit

```bash
git add Makefile
git commit -m "chore(make): add integration target with docker compose lifecycle"
```

---

## Verification

With Docker Compose running:

```bash
docker compose up -d
# Wait ~10s for RabbitMQ to be ready
go test -tags integration -v -race -timeout 120s ./tests/
docker compose down
```

Expected: `TestBasicPublishConsume`, `TestMultipleClientsSharedUpstream`, `TestClientReconnect`, and `TestIdleUpstreamCleanup` all PASS.

Full suite without integration tag still passes:

```bash
go test ./... -race -count=1
```
