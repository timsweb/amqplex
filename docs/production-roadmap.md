# Production Readiness Roadmap

> Status: True connection multiplexing implemented (2026-02-22). Core production gaps narrowed to observability and circuit breakers.

## Critical Gaps (P0) - Must Fix Before Production

### 1. Connection Pool Reuse ✅ DONE (2026-02-22)
**Implemented:** True channel multiplexing via `ManagedUpstream`
- Multiple clients share one upstream AMQP connection with channel ID remapping
- Packing strategy: fill one upstream to `PoolMaxChannels` before opening another (slice-per-credential-set)
- Safe channels released for reuse on client disconnect; unsafe channels explicitly closed on upstream
- `AllocateChannel` / `ReleaseChannel` with lowest-free-ID scan

---

### 2. Upstream Reconnection ✅ DONE (2026-02-22)
**Implemented:** `ManagedUpstream.reconnectLoop` with exponential backoff
- Backoff: 500ms → 1s → 2s → … → 30s cap
- On upstream failure: all registered clients aborted, reconnect loop starts
- Reconnect is transparent — proxy stays up; clients see a disconnect and should retry
- `dialFn` injectable for testability

---

### 3. Observability & Monitoring
**Current State:** Almost nonexistent
- Only basic health check at `/health`
- No metrics
- No structured logging
- No tracing

**Required Changes:**
- [ ] Add metrics endpoint (Prometheus format):
  - `connections_active_total` {vhost, user}
  - `connections_rate` {vhost, user}
  - `channels_active_total` {vhost, user}
  - `frames_proxyed_total` {direction, type}
  - `errors_total` {error_type, vhost}
  - `latency_seconds` {direction, percentile}
- [ ] Implement structured logging (JSON format)
- [ ] Add request ID to all logs for tracing
- [ ] Add error categorization (transient vs permanent)
- [ ] Sensitive data redaction (passwords, tokens)
- [ ] Tests: Verify metrics accuracy

**Impact:** CRITICAL - No visibility into production health/performance

**Estimated Effort:** 8-10 hours

---

## Important Gaps (P1)

### 4. Graceful Shutdown Drain ✅ DONE (2026-02-22)
**Implemented:** `Stop()` with 30s `sync.WaitGroup` drain
- Closes listener (no new connections accepted)
- Aborts all registered client connections (unblocks in-flight reads)
- Closes all upstream connections
- Waits up to 30s for all `handleConnection` goroutines to exit
- SIGTERM/SIGINT handled in `main.go` with proper drain synchronisation

---

### 5. Circuit Breakers
**Current State:** None

**Required Changes:**
- [ ] Implement circuit breaker pattern per upstream
- [ ] Configurable threshold (error rate, consecutive failures)
- [ ] Circuit states: Closed, Open, Half-Open
- [ ] Fallback behavior (reject new connections, queue, etc.)
- [ ] Tests: Circuit tripping and recovery scenarios

**Impact:** MEDIUM - No protection against upstream failures/overload

**Estimated Effort:** 4-6 hours

---

### 6. Resource Limits
**Current State:** Partial
- `PoolMaxChannels` enforced per upstream connection via `ManagedUpstream`
- No max concurrent clients or upstream connections
- No memory/CPU limits

**Required Changes:**
- [ ] Add max concurrent clients per credential set (reject with 503 when exceeded)
- [ ] Add global client connection limit
- [ ] Implement backpressure on connection limit exceeded
- [ ] Tests: Verify limits enforced under load

**Impact:** HIGH - Unbounded upstream connections possible under client flood

**Estimated Effort:** 2-3 hours

---

### 7. Idle Upstream Cleanup (NEW — surfaced 2026-02-22)
**Current State:** None
- `ManagedUpstream` instances are never removed from the slice once created
- When all clients disconnect, the upstream connection stays open indefinitely
- Over time (especially with reconnects), stale empty upstreams accumulate

**Required Changes:**
- [ ] Track last-used time on `ManagedUpstream`
- [ ] Background goroutine (or on-demand check in `getOrCreateManagedUpstream`) removes empty upstreams idle > N seconds
- [ ] Remove from `Proxy.upstreams` slice when empty
- [ ] Tests: Verify idle upstreams are cleaned up

**Impact:** MEDIUM - RabbitMQ connection leak over time

**Estimated Effort:** 2-3 hours

---

### 8. Dead Pool Package Removal (NEW — surfaced 2026-02-22)
**Current State:** `pool/` package is entirely dead code
- `pool.ConnectionPool`, `pool.PooledConnection`, `pool.Channel` are unreferenced outside their own package
- Zero imports from the rest of the codebase

**Required Changes:**
- [ ] Delete `pool/` directory entirely
- [ ] Update `go.mod` / `go.sum` if any indirect deps were pool-specific

**Impact:** LOW - Just maintenance debt, no runtime impact

**Estimated Effort:** 30 minutes

---

## Nice-to-Have (P2)

### 9. Security & Access Control
- [ ] Proxy authentication (optional - require credentials)
- [ ] Authorization policies (who can access which vhost)
- [ ] Rate limiting per credential
- [ ] IP whitelisting/blacklisting
- [ ] Audit logging (who connected when, for how long)

### 10. Configuration Management
- [ ] Hot reload (SIGHUP or `/reload` endpoint)
- [ ] Configuration validation on load
- [ ] Connection timeout settings
- [ ] Documentation for all config options

### 11. Advanced Error Handling
- [ ] Structured error codes
- [ ] Error categorization
- [ ] Log levels (debug, info, warn, error)
- [ ] Sensitive data redaction in logs

### 12. Performance Optimizations
- [ ] Channel pooling (pre-open channels for common operations)
- [ ] Message batching
- [ ] Zero-copy frame forwarding where possible

### 13. Operational Features
- [ ] Debug endpoints: `/debug/pprof`, `/debug/connections`
- [ ] Connection statistics: `/stats` endpoint
- [ ] Admin API: list connections, disconnect clients

### 14. Protocol Features
- [x] Heartbeat handling — proxy echoes upstream heartbeats; client heartbeats consumed/discarded (2026-02-22)
- [ ] Flow control (basic.qos-sync)
- [ ] Publisher confirms (basic.ack)
- [ ] Consumer acknowledgments (basic.ack, basic.reject)

### 15. Documentation
- [ ] Deployment guide
- [ ] Operational runbooks
- [ ] Performance tuning guide
- [ ] Troubleshooting guide

---

## Completed Features ✅

- [x] AMQP frame serialization (Connection.Start, Tune, Open)
- [x] Client handshake handling (extract credentials, vhost)
- [x] Upstream AMQP handshake (connect to RabbitMQ)
- [x] Bidirectional frame proxying with channel remapping
- [x] mTLS support (server and client)
- [x] True connection multiplexing — multiple clients share one upstream connection (2026-02-22)
- [x] Channel ID remapping with packing strategy (2026-02-22)
- [x] Safe/unsafe channel classification and cleanup on disconnect (2026-02-22)
- [x] Upstream reconnection with exponential backoff (2026-02-22)
- [x] Heartbeat proxying — echo upstream, discard client (2026-02-22)
- [x] Graceful shutdown drain — 30s WaitGroup + SIGTERM/SIGINT (2026-02-22)
- [x] Channel mapping and cleanup
- [x] Concurrent connection handling (tests)
- [x] Channel lifecycle management (tests)

---

## Metrics to Implement

### Connection Metrics
```
connections_active_total{vhost, user}
connections_created_total{vhost, user}
connections_closed_total{vhost, user, reason}
connections_rate{vhost, user}
```

### Channel Metrics
```
channels_active_total{vhost, user}
channels_created_total{vhost, user}
channels_closed_total{vhost, user}
```

### Frame Metrics
```
frames_proxyed_total{direction, frame_type}
frames_failed_total{direction, reason}
```

### Error Metrics
```
errors_total{error_type, vhost, user}
upstream_reconnects_total{vhost, user}
```

### Latency Metrics
```
proxy_latency_seconds{direction, p50, p95, p99}
handshake_duration_seconds{vhost}
```

---

## Priority Order for Production Readiness

1. **P0-3: Observability & Monitoring** - Critical for production visibility
2. **P0-1: Connection Pool Reuse** - Make pool actually useful
3. **P0-2: Upstream Reconnection** - Prevent cascading failures
4. **P1-6: Resource Limits** - Prevent resource exhaustion
5. **P1-4: Graceful Shutdown Drain** - Better shutdown experience

---

## Testing Checklist

### Unit Tests
- [x] Frame serialization
- [x] Channel mapping/remapping
- [x] Connection pooling basic
- [ ] Connection reuse logic
- [x] Reconnection logic (2026-02-22)
- [ ] Circuit breaker states
- [ ] Resource limit enforcement
- [ ] Error categorization

### Integration Tests
- [x] Basic proxy flow (client → upstream → client)
- [x] Concurrent connections
- [x] TLS connections
- [x] Upstream reconnection scenarios (unit tested; integration test against real RabbitMQ TODO)
- [ ] Circuit breaker tripping
- [ ] Load testing with real RabbitMQ
- [ ] End-to-end message flow (publish/confirm/consume)

### Load Tests
- [ ] Max concurrent connections
- [ ] Sustained throughput (messages/sec)
- [ ] Memory usage under load
- [ ] CPU usage under load
- [ ] Latency percentiles (p50, p95, p99)

---

## References

- Original code review plan: `docs/plans/2026-02-21-amqproxy-fixes.md`
- AMQP 0-9-1 specification: https://www.rabbitmq.com/resources/specs/amqp0-9-1.pdf
- RabbitMQ production deployment guide: https://www.rabbitmq.com/production-checklist.html
- Prometheus best practices: https://prometheus.io/docs/practices/
