# Publish Buffer Design

## Goal

Buffer Basic.Publish messages on `ManagedUpstream` during upstream reconnect windows so that short-lived publisher clients (e.g. PHP fire-and-forget) complete their publish-disconnect cycle successfully rather than receiving a 503. Buffered messages are drained to the upstream once the connection is re-established.

## Background

The proxy multiplexes many short-lived client connections onto a small pool of long-lived upstream connections. When the upstream goes down (e.g. a VPN blip), clients receive a 503 and their publish is lost. PHP applications that lack reconnect logic fail permanently. Events like `product.viewed` are silently dropped.

The solution is an in-memory buffer on `ManagedUpstream`. The proxy accepts the client's full session during an outage, buffers the publish, and drains it when the upstream reconnects.

**Out of scope:** durable (disk-backed) buffering. For genuinely critical events (e.g. `order.placed`), the correct solution is a transactional outbox in the application. The proxy buffer is a best-effort mechanism for handling brief outages.

## Prerequisite PHP Change

The buffer only covers the hot publish path: **Channel.Open → Basic.Publish → disconnect**. Any synchronous method that requires a real server response (Queue.Declare, Exchange.Declare) will hang waiting for a response during an outage. Callers must move Exchange.Declare (and any Queue.Declare) out of the per-request hot path into application startup/bootstrap.

## Config

```toml
[pool]
publish_buffer_size = 1000   # max messages to buffer; 0 = disabled (default)
```

- Exposed as `pool.publish_buffer_size` / env `POOL_PUBLISH_BUFFER_SIZE`.
- Cap is in **messages** (complete publish units), not bytes.
- Default is `0` — feature is opt-in; existing behaviour is preserved.

## Architecture

### Buffer unit: `bufferedMessage`

Basic.Publish is a multi-frame AMQP message: a method frame, a content header frame, and one or more body frames. The buffer stores complete messages as a unit to avoid interleaving issues during drain.

```go
type bufferedMessage struct {
    method *Frame   // Basic.Publish method frame
    header *Frame   // content header frame
    bodies []*Frame // body frame(s)
}
```

### Buffer location

`ManagedUpstream` gains a `publishBuffer []bufferedMessage` (slice, protected by `mu`) and a `bufferSize int` field (set from config, 0 = disabled).

### Buffer mode activation

Buffer mode is active when `conn == nil` (upstream is reconnecting) and `bufferSize > 0`.

### Synthetic Channel.OpenOk

When the upstream is reconnecting and a client sends `Channel.Open`, the proxy cannot forward it. Instead, `handleConnection` detects this case and writes a synthetic `Channel.OpenOk` directly to the client. This is safe: the frame carries no application state, and the channel will be genuinely opened on the upstream during drain.

All other synchronous methods (Queue.Declare, Exchange.Declare, etc.) are forwarded normally. If the upstream is down they receive no response and the client times out — this is intentional and correct behaviour.

### Frame accumulation

`handleConnection` accumulates frames into a complete `bufferedMessage`:

1. Detect `Basic.Publish` method frame → begin accumulating.
2. Read the content header frame → record body size.
3. Read body frames until accumulated size equals declared body size → message complete.
4. Add complete message to `ManagedUpstream.publishBuffer` under lock.

### Buffer overflow

When `len(publishBuffer) >= bufferSize`:

- New client connections are rejected with `Connection.Close 503` (same as today's "upstream unavailable" path).
- Clients mid-session that attempt to publish have their connection dropped.
- Messages already in the buffer are preserved.
- A warning is logged at the point the buffer fills.

### Drain on reconnect

After `reconnectLoop` establishes a new upstream connection, before launching `readLoop`:

1. Check `len(publishBuffer) > 0`. If empty, skip drain.
2. Open a dedicated drain channel on the new upstream (`Channel.Open` / wait for `Channel.OpenOk`).
3. Replay all buffered messages in order, remapping each frame's channel ID to the drain channel.
4. Close the drain channel.
5. Clear `publishBuffer`.
6. Launch `readLoop` and resume normal operation.

If the upstream drops again during drain, the remaining buffered messages are kept and the reconnect/drain cycle repeats. Nothing already buffered is lost unless the proxy process itself exits.

### Logging

| Event | Level |
|---|---|
| Buffer mode entered (upstream reconnecting, buffer enabled) | `INFO` |
| Message buffered (include buffer depth) | `DEBUG` |
| Buffer full — new publishes rejected | `WARN` |
| Drain started (N messages) | `INFO` |
| Drain complete | `INFO` |
| Drain interrupted by upstream failure | `WARN` |

## Known Limitations

- **In-memory only.** Buffer is lost if the proxy process exits. For critical business events use an application-level transactional outbox.
- **Hot path only.** Queue.Declare / Exchange.Declare in the per-request path will timeout during an outage. Callers must move these to startup.
- **No publisher confirms.** Fire-and-forget (no `Confirm.Select`) only.
- **Consumers unaffected.** Consumer channels are still aborted when the upstream fails, as today.

## Testing

**Unit tests (`proxy/managed_upstream_test.go`)**

- Messages are buffered when `conn == nil` and `bufferSize > 0`.
- Buffer disabled when `bufferSize == 0`.
- Overflow correctly rejects at capacity.
- Drain replays all messages in order on reconnect.
- Drain survives a second upstream failure mid-drain (messages preserved for next attempt).

**Integration test (`tests/integration_e2e_test.go`)**

- Start proxy with `PublishBufferSize = 10`.
- Publish 5 messages while upstream is down (proxy should accept all 5 sessions cleanly).
- Bring upstream back up.
- Consume from queue and assert all 5 messages arrived in order.
