# Production Readiness Roadmap

> Status: Core AMQP proxy functionality implemented, but several production-critical gaps remain.

## Critical Gaps (P0) - Must Fix Before Production

### 1. Connection Pool Reuse
**Current State:** Pool exists but provides no value
- `GetConnection()` always returns `pool.Connections[0]`
- No load balancing across multiple connections
- No health checking of pooled connections
- No eviction of idle/dead connections

**Required Changes:**
- [ ] Implement connection selection strategy (round-robin, least-connections, etc.)
- [ ] Add connection health checking (periodic ping/pong)
- [ ] Implement idle timeout eviction
- [ ] Add connection reuse logic in `handleConnection`
- [ ] Tests for connection reuse under load

**Impact:** HIGH - Pool provides no value, always using first connection

**Estimated Effort:** 4-6 hours

---

### 2. Upstream Reconnection
**Current State:** None - connection fail = client disconnect
```go
// handleConnection returns immediately on any upstream error
if err != nil {
    return  // Client disconnected!
}
```

**Required Changes:**
- [ ] Implement automatic reconnection with exponential backoff
- [ ] Track upstream connection state
- [ ] Buffer client messages during reconnect
- [ ] Send connection errors back to client if reconnect fails
- [ ] Tests: Upstream blip + reconnect scenarios
- [ ] Tests: Permanent upstream failure handling

**Impact:** CRITICAL - Any upstream blip drops all connected clients

**Estimated Effort:** 6-8 hours

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

### 4. Graceful Shutdown Drain
**Current State:** Partial
- `Stop()` closes listener and pools
- No drain period
- In-flight operations may fail abruptly

**Required Changes:**
- [ ] Implement drain period (wait for connections to complete)
- [ ] Add max wait time (force shutdown after N seconds)
- [ ] Log number of active connections during shutdown
- [ ] Tests: Verify in-flight operations complete during drain

**Impact:** MEDIUM - Clients may see abrupt connection close

**Estimated Effort:** 2-3 hours

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
- `PoolMaxChannels` exists but not enforced
- No max concurrent connections
- No memory/CPU limits

**Required Changes:**
- [ ] Enforce `MaxChannels` per connection
- [ ] Add max concurrent connections per user/vhost
- [ ] Add global connection limit
- [ ] Implement backpressure on connection limit exceeded
- [ ] Tests: Verify limits enforced under load

**Impact:** HIGH - Resource exhaustion possible

**Estimated Effort:** 3-4 hours

---

## Nice-to-Have (P2)

### 7. Security & Access Control
- [ ] Proxy authentication (optional - require credentials)
- [ ] Authorization policies (who can access which vhost)
- [ ] Rate limiting per credential
- [ ] IP whitelisting/blacklisting
- [ ] Audit logging (who connected when, for how long)

### 8. Configuration Management
- [ ] Hot reload (SIGHUP or `/reload` endpoint)
- [ ] Configuration validation on load
- [ ] Connection timeout settings
- [ ] Documentation for all config options

### 9. Advanced Error Handling
- [ ] Structured error codes
- [ ] Error categorization
- [ ] Log levels (debug, info, warn, error)
- [ ] Sensitive data redaction in logs

### 10. Performance Optimizations
- [ ] Channel pooling (pre-open channels for common operations)
- [ ] Message batching
- [ ] Zero-copy frame forwarding where possible

### 11. Operational Features
- [ ] Debug endpoints: `/debug/pprof`, `/debug/connections`
- [ ] Connection statistics: `/stats` endpoint
- [ ] Admin API: list connections, disconnect clients

### 12. Protocol Features
- [ ] Heartbeat handling (currently ignored)
- [ ] Flow control (basic.qos-sync)
- [ ] Publisher confirms (basic.ack)
- [ ] Consumer acknowledgments (basic.ack, basic.reject)

### 13. Documentation
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
- [x] Connection pooling structure
- [x] Graceful shutdown (Stop() method)
- [x] Channel mapping and cleanup
- [x] Concurrent connection handling (tests)
- [x] Channel lifecycle management (tests)
- [x] Connection pool lifecycle (tests)

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
- [ ] Reconnection logic
- [ ] Circuit breaker states
- [ ] Resource limit enforcement
- [ ] Error categorization

### Integration Tests
- [x] Basic proxy flow (client → upstream → client)
- [x] Concurrent connections
- [x] TLS connections
- [ ] Upstream reconnection scenarios
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
