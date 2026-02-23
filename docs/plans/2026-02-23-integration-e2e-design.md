# Integration E2E Test Suite Design

**Goal:** Add end-to-end integration tests that verify publish/consume correctness through the proxy using real AMQP clients and a live RabbitMQ broker.

---

## Structure

- New file: `tests/integration_e2e_test.go`
- Build tag: `//go:build integration` (opt-in, not run by default `go test ./...`)
- `TestMain` in `tests/` waits for the proxy to be reachable on `localhost:5673` (TCP retry loop, 30s timeout) before running any test
- New Makefile target `integration`: `docker compose up -d && go test -tags integration -v ./tests/ && docker compose down`

Tests 1–3 require the Docker Compose stack to be running (proxy + RabbitMQ). Test 4 spins up an in-process proxy.

---

## Client Library

`github.com/rabbitmq/amqp091-go` — already present in `go.mod` as an indirect dependency. Promoted to direct for the integration tests.

All connections dial `amqp://guest:guest@localhost:5673/`.

---

## Test Cases

### 1. `TestBasicPublishConsume`
Declare a transient auto-delete queue, publish one message, consume it with `channel.Get`, assert body matches. Queue is cleaned up on test exit.

### 2. `TestMultipleClientsSharedUpstream`
Open two separate AMQP connections (same credentials → same `ManagedUpstream`). Each opens its own channel, publishes to the same queue, consumes all messages. Verifies channel multiplexing works correctly across clients.

### 3. `TestClientReconnect`
Connect, publish a message, close the connection, reconnect, publish a second message, consume both. Verifies the proxy handles client disconnect/reconnect cleanly.

### 4. `TestIdleUpstreamCleanup`
Start proxy in-process with `PoolIdleTimeout: 1`. Connect via `amqp091-go`, publish, disconnect. Wait 2s for idle cleanup. Reconnect and publish/consume again — verifies a new upstream is created after cleanup.

---

## Helpers

- `dialProxy(t) *amqp.Connection` — retry loop (5 attempts, 200ms apart), registers `t.Cleanup(conn.Close())`
- `declareQueue(t, ch, name) amqp.Queue` — declares non-durable auto-delete queue, registers cleanup

---

## Error Handling

All `amqp091-go` calls use `require` (fatal on error). No silent swallowing.

---

## Execution

```bash
# Bring up stack and run integration tests
make integration

# Or manually:
docker compose up -d
go test -tags integration -v ./tests/
docker compose down
```
