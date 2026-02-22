# Structured Logging Design

**Date:** 2026-02-22
**Feature:** Structured logging via `log/slog`

---

## Goal

Add meaningful, structured log output to AMQProxy so operators can see what the proxy is doing in production — which clients are connected, when upstreams go down and come back, and how long drain takes — without any new dependencies.

---

## Key Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Library | stdlib `log/slog` | No new deps; supports text and JSON handlers natively |
| Injection | Pass `*slog.Logger` into `NewProxy` | Testable; tests can pass `slog.New(slog.NewTextHandler(io.Discard, nil))` to silence output |
| Format | Text by default, JSON via `LOG_FORMAT=json` | Text for supervisor/terminal; JSON for Docker/k8s log aggregators |
| Level | Info by default, `LOG_LEVEL` env var to override | Debug covers channel alloc/release; info covers connect/disconnect/upstream events |
| Passwords | Never logged | `user` is safe; password is not |

---

## Logger Construction (main.go)

```go
level := slog.LevelInfo
if strings.ToLower(os.Getenv("LOG_LEVEL")) == "debug" {
    level = slog.LevelDebug
} else if strings.ToLower(os.Getenv("LOG_LEVEL")) == "warn" {
    level = slog.LevelWarn
}

opts := &slog.HandlerOptions{Level: level}
var handler slog.Handler
if strings.ToLower(os.Getenv("LOG_FORMAT")) == "json" {
    handler = slog.NewJSONHandler(os.Stdout, opts)
} else {
    handler = slog.NewTextHandler(os.Stdout, opts)
}
logger := slog.New(handler)
```

Passed to `proxy.NewProxy(cfg, logger)`.

---

## Log Events

| Event | Level | Fields |
|---|---|---|
| Proxy started | INFO | `addr` |
| Proxy stopped (drain complete) | INFO | `elapsed_ms` |
| Client connected | INFO | `remote_addr`, `user`, `vhost` |
| Client disconnected | INFO | `remote_addr`, `user`, `vhost`, `duration_ms` |
| Upstream connected | INFO | `upstream_addr`, `user`, `vhost` |
| Upstream lost | WARN | `upstream_addr`, `user`, `vhost`, `error` |
| Upstream reconnecting | WARN | `upstream_addr`, `user`, `vhost`, `attempt`, `backoff_ms` |
| Upstream reconnected | INFO | `upstream_addr`, `user`, `vhost`, `attempt` |
| Client rejected — upstream unavailable | WARN | `remote_addr`, `error` |
| Channel allocated | DEBUG | `client_chan`, `upstream_chan`, `user`, `vhost` |
| Channel released | DEBUG | `upstream_chan`, `safe`, `user`, `vhost` |

---

## Struct Changes

```go
// Proxy gains a logger field
type Proxy struct {
    // ...existing fields...
    logger *slog.Logger
}

// ManagedUpstream gains a logger field (passed from Proxy at creation)
type ManagedUpstream struct {
    // ...existing fields...
    logger *slog.Logger
}
```

`NewProxy(cfg *config.Config, logger *slog.Logger) (*Proxy, error)` — logger is required; callers that don't care pass `slog.Default()`.

---

## What This Is Not

- No metrics (separate P0 item)
- No request IDs / trace correlation (future)
- No log rotation (handled by supervisor/Docker)
- No per-connection log sampling (future)
