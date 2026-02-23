# Limits Design

**Date:** 2026-02-23
**Feature:** Upstream connection limit and client connection limit

---

## Goal

Prevent the proxy from opening unbounded upstream AMQP connections to RabbitMQ, and from accepting unbounded concurrent client TCP connections. Both limits protect downstream resources and are operator-configurable, with 0 meaning unlimited (fully backwards compatible).

---

## New Config Fields

```go
MaxUpstreamConnections int  // pool.max_connections env/config key, default 0 (unlimited)
MaxClientConnections   int  // max_client_connections env/config key, default 0 (unlimited)
```

Both default to 0 (unlimited) to preserve existing behaviour.

---

## Client Connection Limit

### Mechanism

`Proxy` gains an `activeClients atomic.Int32` counter. In `Start()`, inside the accept loop, after the `netListener == nil` guard and `wg.Add(1)` but before launching the goroutine:

```go
if p.config.MaxClientConnections > 0 &&
    int(p.activeClients.Load()) >= p.config.MaxClientConnections {
    conn.Close()
    continue
}
p.activeClients.Add(1)
```

`handleConnection` decrements unconditionally on exit:

```go
defer p.activeClients.Add(-1)
```

### Rejection behaviour

At TCP accept time no AMQP handshake has occurred, so the rejection is a bare TCP close. AMQP client libraries treat an immediate close as a transient failure and retry with backoff. This is consistent with how the OS itself rejects connections when the accept backlog is full.

### Why at accept time

Enforcing before the goroutine is spawned prevents resource consumption (goroutine stack, buffered reader/writer allocation) from beginning at all. The check is a single atomic load — negligible overhead.

---

## Upstream Connection Limit

### Mechanism

In `getOrCreateManagedUpstream`, immediately before dialing a new upstream connection (under the existing `p.mu.Lock()`), count total `ManagedUpstream` instances across all credential sets:

```go
if p.config.MaxUpstreamConnections > 0 {
    total := 0
    for _, slice := range p.upstreams {
        total += len(slice)
    }
    if total >= p.config.MaxUpstreamConnections {
        return nil, errors.New("upstream connection limit reached")
    }
}
```

### Rejection behaviour

The error propagates up through `handleConnection`'s existing "upstream unavailable" path, which sends `Connection.Close` with reply code 503 and the message from the error, then closes the TCP connection. The client sees a proper AMQP-level rejection.

### Why global

A global cap directly models the resource being protected: the number of TCP connections open to RabbitMQ. Per-credential caps would require additional config complexity and don't map naturally to RabbitMQ's connection limit settings.

### Lock ordering

The count scan happens inside `p.mu.Lock()`, which is already held at this point in `getOrCreateManagedUpstream`. No additional locking is required.

---

## Files Changed

- `config/config.go` — add two new fields and defaults
- `proxy/proxy.go` — add `activeClients` counter; enforce client limit in `Start()`; enforce upstream limit in `getOrCreateManagedUpstream()`; decrement counter in `handleConnection()`

---

## Testing

### Unit tests (no real RabbitMQ needed)

- `TestClientLimitRejectsOverLimit` — pre-set `activeClients` to `MaxClientConnections`, accept a connection, verify it is immediately closed
- `TestClientLimitAllowsUnderLimit` — verify connections are accepted when under limit
- `TestUpstreamLimitRejectsNewDial` — pre-populate `p.upstreams` with N stopped upstreams up to the limit, verify `getOrCreateManagedUpstream` returns an error without dialing
- `TestUpstreamLimitAllowsUnderLimit` — verify a new upstream is dialled when under limit
- `TestLimitZeroMeansUnlimited` — verify that `MaxUpstreamConnections=0` and `MaxClientConnections=0` impose no limit

---

## What This Is Not

- No per-credential-set upstream cap (global only)
- No queuing/backpressure — rejection is immediate
- No rate limiting (connections per second) — this is a concurrency cap only
