# OpenMetrics Endpoint Design

## Goal

Expose a `/metrics` endpoint in Prometheus text format so the New Relic Infrastructure agent (and any Prometheus-compatible scraper) can observe proxy health and load.

## Background

The proxy multiplexes many short-lived client connections onto a small pool of long-lived upstream connections. Operators need visibility into connection pressure, channel utilisation, and upstream health without tailing logs. The New Relic Infrastructure agent's Prometheus integration scrapes a standard `/metrics` endpoint — no library needed, just valid Prometheus text format.

## Config

No new config fields. The endpoint is served on the existing health port (`ListenPort+1`) alongside the existing health check.

## Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `amqproxy_active_clients` | gauge | Current number of connected clients |
| `amqproxy_upstream_connections` | gauge | Total upstream connections (all credential sets) |
| `amqproxy_upstream_reconnecting` | gauge | Upstream connections currently in reconnect loop |
| `amqproxy_channels_used` | gauge | Total AMQP channels currently allocated |
| `amqproxy_channels_pending_close` | gauge | Channels awaiting Channel.CloseOk from broker |
| `amqproxy_upstream_reconnect_attempts_total` | counter | Cumulative upstream reconnect attempts since proxy start |

All gauges are derived from existing struct state (no new fields). The counter requires one new `atomic.Int64` field (`reconnectTotal`) on `ManagedUpstream`, incremented each loop iteration in `reconnectLoop`.

## Architecture

### New state

`ManagedUpstream` gains one field:

```go
reconnectTotal atomic.Int64 // incremented each reconnect attempt
```

### New file: `proxy/metrics.go`

`MetricsHandler() http.Handler` method on `Proxy`:

1. Acquires `p.mu.RLock()`
2. Reads `activeClients`, iterates `p.upstreams` to compute the five upstream-derived values
3. Releases lock
4. Writes Prometheus text format to the response with `Content-Type: text/plain; version=0.0.4; charset=utf-8`

Format example:

```
# HELP amqproxy_active_clients Current number of connected clients.
# TYPE amqproxy_active_clients gauge
amqproxy_active_clients 12
# HELP amqproxy_upstream_connections Total upstream AMQP connections.
# TYPE amqproxy_upstream_connections gauge
amqproxy_upstream_connections 3
...
```

### `main.go` change

Swap bare handler for a mux:

```go
mux := http.NewServeMux()
mux.Handle("/", health.Handler())
mux.Handle("/metrics", p.MetricsHandler())
go http.ListenAndServe(addr, mux)
```

No changes to `health/health.go`.

## Testing

**Unit tests (`proxy/metrics_test.go`)**

- Build a `Proxy` with controlled upstream state (some reconnecting, some healthy, channels allocated)
- Assert response status 200, correct `Content-Type`, and that each metric name and value appears correctly in the body

## Known Limitations

- Metrics are collected on each scrape (no caching). At typical scrape intervals (15–60s) the read lock contention is negligible.
- No per-vhost or per-user labels — aggregate only. Per-label breakdown can be added later if needed.
- No process/Go runtime metrics (goroutines, GC, memory). Add via a separate `/metrics` extension if needed.
