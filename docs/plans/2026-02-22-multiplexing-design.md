# AMQProxy Multiplexing Design

**Date:** 2026-02-22
**Features:** Connection pool reuse, upstream reconnection, graceful shutdown drain, heartbeat proxying
**Inspired by:** [cloudamqp/amqplex](https://github.com/cloudamqp/amqplex) (Crystal)

---

## Goal

Make the proxy actually useful in production by implementing true connection multiplexing: multiple clients share a single upstream AMQP connection, with channel IDs remapped into non-overlapping ranges. Combined with upstream reconnection, proxy-owned heartbeats, and a graceful shutdown drain, this makes AMQProxy a reliable intermediary that reduces RabbitMQ connection count while surviving upstream blips.

---

## Key Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Multiplexing model | True channel sharing (multiple clients per upstream connection) | Core value proposition — matches CloudAMQP original |
| Connection packing | Fill one upstream to MaxChannels before opening another | Minimises total upstream connections |
| Reconnection model | Disconnect all affected clients, reconnect with backoff | Transparent reconnect is unsafe for AMQP (unacked messages, consumer state can't be replayed) |
| Heartbeat | Proxy echoes upstream heartbeats; consumes client heartbeats | Matches CloudAMQP original; prevents competing heartbeat sources |
| Shutdown drain | Fixed 30s timeout, no config knob | Covers 99% of cases; can be made configurable later |

---

## Core Architecture: `ManagedUpstream`

### The Problem

Today, `handleConnection` dials its own private upstream TCP connection and owns a private bidirectional read loop. With multiplexing, multiple clients must share one upstream connection — a single goroutine must own reading from the upstream and dispatch frames to the right client.

### The Solution

Introduce `proxy.ManagedUpstream` — one instance per `(username, password, vhost)` credential set — that owns the upstream connection lifecycle.

`Proxy` replaces its `map[[32]byte]*pool.ConnectionPool` with `map[[32]byte]*ManagedUpstream`.

```
Proxy
  └── ManagedUpstream [per credential set]
        ├── *UpstreamConn          (upstream TCP connection)
        ├── readLoop goroutine     (sole reader of upstream frames)
        ├── channelOwners map      (upstreamChannelID → *ClientConnection)
        ├── usedChannels map       (upstreamChannelID → bool)
        └── reconnect logic
```

### Data Flow

```
Client TCP conn → handleConnection goroutine:
  1. Client AMQP handshake (unchanged)
  2. Register with ManagedUpstream
  3. Client→upstream loop only:
       - Detect Channel.Open  → AllocateChannel
       - Detect Channel.Close → ReleaseChannel
       - All frames           → remap channel ID, write to upstream

ManagedUpstream.readLoop goroutine (one per upstream connection):
  - Frame type=8 (heartbeat)    → echo back to upstream, discard
  - Channel 0, non-heartbeat    → dispatch to all clients (Connection.Close etc.)
  - Other channels              → look up owner in channelOwners, write remapped frame to client writer
  - Read error                  → tear down all clients, reconnect with backoff
```

### Package Changes

- `pool.ConnectionPool`, `pool.PooledConnection`, `pool.Connection` interface — removed (replaced by `ManagedUpstream`)
- `pool.Channel` — kept (safe/unsafe operation tracking still used for channel reuse decisions)
- `proxy/managed_upstream.go` — new file containing `ManagedUpstream`
- `proxy/proxy.go` — updated to use `ManagedUpstream` and `sync.WaitGroup` for drain

---

## Feature 1: Connection Pool Reuse

### Channel Allocation

`ManagedUpstream` exposes:

```go
// AllocateChannel finds the lowest unused upstream channel ID, registers the
// mapping on cc, and returns the upstream ID. Returns an error if MaxChannels
// is exhausted (caller should try the next ManagedUpstream or create a new one).
func (m *ManagedUpstream) AllocateChannel(clientID uint16, cc *ClientConnection) (uint16, error)

// ReleaseChannel marks the upstream channel ID as free and removes it from
// the dispatch map.
func (m *ManagedUpstream) ReleaseChannel(upstreamID uint16)
```

`usedChannels` is a `map[uint16]bool` under a `sync.RWMutex`. Allocation scans for the lowest free ID starting at 1 (channel 0 is reserved for connection-level frames).

### Packing Strategy

`getOrCreateManagedUpstream` (replaces `getOrCreatePool`):
1. Look up existing `ManagedUpstream` for the credential key
2. If it exists and has spare channel capacity (`len(usedChannels) < MaxChannels`): return it
3. If it exists but is full: dial a new upstream, create a second `ManagedUpstream` for the same credential key (stored in a slice, not a single value)
4. If none exists: dial upstream, create `ManagedUpstream`

### Channel Detection in `handleConnection`

The client→upstream loop inspects each frame's method header to detect:
- `Channel.Open` (class=20, method=10): call `AllocateChannel` before forwarding
- `Channel.Close` (class=20, method=40) and `Channel.CloseOk` (class=20, method=41): call `ReleaseChannel` after forwarding

All other frames are remapped and forwarded without further inspection.

### Safe Channel Release on Client Disconnect

When a client disconnects, for each of its mapped channels:
- Check `pool.Channel.IsSafe()` — only `Basic.Publish` and `Basic.Get` (no-ack) occurred
- **Safe**: call `ReleaseChannel` — the upstream channel ID is free for the next client
- **Unsafe**: send `Channel.Close` (class=20, method=40) to the upstream before calling `ReleaseChannel`

---

## Feature 2: Upstream Reconnection

### Failure Path

When `ManagedUpstream.readLoop` gets a read error:

1. Set `reconnecting` atomic flag to `true`
2. Lock client list; close the `net.Conn` of every registered `ClientConnection` — their client→upstream write loops exit with an error and clean themselves up (deregister, release channels)
3. Close the dead upstream connection
4. Enter reconnect loop

### Reconnect Loop

```
backoff: 500ms → 1s → 2s → 4s → 8s → 16s → 30s (capped)

loop:
  sleep(backoff)
  dial TCP to upstream addr
  if error: double backoff, continue
  performUpstreamHandshake(conn, username, password, vhost)
  if error: close conn, double backoff, continue
  replace m.conn with new UpstreamConn
  clear reconnecting flag
  restart readLoop
  return
```

Reconnect attempts continue indefinitely. The proxy stays up throughout.

### Behaviour During Reconnection

Clients that attempt to connect while `ManagedUpstream` is reconnecting receive `Connection.Close` with reply code 503 (service unavailable) and a descriptive message. They are not queued — the AMQP client library's own reconnect logic should retry.

### Idle ManagedUpstream

If a `ManagedUpstream` has no registered clients and no allocated channels when its upstream connection fails (idle pool connection), it reconnects silently with the same backoff — no client teardown needed.

---

## Feature 3: Heartbeat Proxying

### Upstream Heartbeats

In `ManagedUpstream.readLoop`, when `frame.Type == FrameTypeHeartbeat`:
- Write a heartbeat frame back to the upstream immediately
- Do **not** forward to any client

This keeps the upstream connection alive regardless of client activity, identical to the CloudAMQP Crystal implementation.

### Client Heartbeats

In `handleConnection`'s client→upstream loop, channel-0 frames are inspected:
- `frame.Type == FrameTypeHeartbeat`: consume and discard — the proxy owns the upstream heartbeat
- All other channel-0 frames (e.g. `Connection.Close` from client): forward to upstream as normal

### Heartbeat Interval

During `performUpstreamHandshake`, the upstream's `Connection.Tune` heartbeat value is stored on `ManagedUpstream`. This is available for future use (e.g. a timer-based sender). For now, responding to upstream-initiated heartbeats is sufficient.

---

## Feature 4: Graceful Shutdown Drain

### Client Tracking

`Proxy` gains a `sync.WaitGroup`. `handleConnection` calls `wg.Add(1)` on entry and `wg.Done()` on exit.

### Updated `Stop()` Sequence

```
1. Close net.Listener         — no new clients accepted
2. For each ManagedUpstream:
     - Set stopped flag (prevents reconnect loop from restarting)
     - Close upstream net.Conn (read loop exits → tears down all clients)
3. wg.Wait() with 30s timeout — wait for all handleConnection goroutines to exit
4. Return (process can now exit cleanly)
```

### Signal Handling

`main.go` adds a `signal.NotifyContext` (or `signal.Notify`) for `SIGTERM` and `SIGINT`. On signal receipt, `p.Stop()` is called. This gives Kubernetes rolling deploys and `Ctrl-C` the full drain behaviour.

---

## Testing Plan

### Unit Tests
- `ManagedUpstream.AllocateChannel` — fills channels, returns error when full
- `ManagedUpstream.ReleaseChannel` — channel becomes available after release
- `ManagedUpstream.readLoop` heartbeat handling — echoes heartbeat, doesn't forward to clients
- Channel open/close detection in `handleConnection` client→upstream loop
- Safe vs unsafe channel release on client disconnect

### Integration Tests
- Multiple clients share one upstream connection (verify connection count at RabbitMQ)
- Client disconnect releases safe channels (next client reuses them)
- Client disconnect closes unsafe channels on upstream
- Upstream failure tears down all clients
- Proxy reconnects after upstream restart (with mock upstream)
- `Stop()` waits for in-flight clients up to 30s
- `SIGTERM` triggers graceful drain

---

## What This Is Not

- **No transparent reconnect**: clients see a disconnect on upstream failure and must reconnect themselves
- **No message buffering**: in-flight messages on a failed upstream are lost
- **No circuit breaker**: that is a separate P1 item
- **No metrics**: observability is a separate P0 item
- **No configurable drain timeout**: fixed 30s; make configurable later if needed
