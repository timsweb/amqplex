# AMQPlex

> **Experimental:** AMQPlex is under active development and not yet recommended for mission-critical production deployments. APIs and configuration may change between releases.

High-performance AMQP 0-9-1 multiplexer. Multiplexes many short-lived client connections onto a small pool of long-lived upstream connections, reducing connection churn on RabbitMQ — especially useful for PHP and other stateless request-per-process workloads.

## Features

- **Connection multiplexing** — multiple clients share one upstream AMQP connection via channel ID remapping
- **Connection pooling** — one pool per credential set (`username:password:vhost`), fills channels before opening new upstreams
- **Upstream reconnection** — exponential backoff (500ms → 30s cap) with automatic client abort on failure
- **Connection limits** — configurable max client and upstream connection counts
- **Idle upstream cleanup** — upstream connections idle beyond a timeout are closed and removed
- **TLS/mTLS** — server TLS for clients; client TLS for upstream; OS trust store, custom CA, or env-var certs
- **Health check** — `GET /` on `listen.port + 1` returns `200 OK`
- **OpenMetrics** — `GET /metrics` on `listen.port + 1` in Prometheus text format

## Usage

```bash
# Build
make build

# Run with config file
./bin/amqplex --config config.toml

# Run with upstream URL (uses defaults for everything else)
./bin/amqplex amqps://rabbitmq:5671
```

## Configuration

Config file (TOML) or environment variables (prefix `AMQP_`, dots replaced with underscores).

| Key | Env | Default | Description |
|-----|-----|---------|-------------|
| `listen.address` | `AMQP_LISTEN_ADDRESS` | `0.0.0.0` | Listen address |
| `listen.port` | `AMQP_LISTEN_PORT` | `5673` | AMQP listen port (health/metrics on port+1) |
| `upstream.url` | `AMQP_UPSTREAM_URL` | — | Upstream RabbitMQ URL (`amqp://` or `amqps://`) |
| `pool.max_channels` | `AMQP_POOL_MAX_CHANNELS` | `65535` | Max channels per upstream connection |
| `pool.idle_timeout` | `AMQP_POOL_IDLE_TIMEOUT` | `5` | Seconds before an idle upstream is closed |
| `pool.cleanup_interval` | `AMQP_POOL_CLEANUP_INTERVAL` | `30` | Idle cleanup sweep interval (seconds) |
| `pool.max_upstream_connections` | `AMQP_POOL_MAX_UPSTREAM_CONNECTIONS` | `0` | Max upstream connections (0 = unlimited) |
| `pool.max_client_connections` | `AMQP_POOL_MAX_CLIENT_CONNECTIONS` | `0` | Max client connections (0 = unlimited) |
| `tls.cert` | `AMQP_TLS_CERT` | — | Server TLS certificate (PEM path or base64) |
| `tls.key` | `AMQP_TLS_KEY` | — | Server TLS key (PEM path or base64) |
| `tls.ca_cert` | `AMQP_TLS_CA_CERT` | — | Upstream CA certificate |
| `tls.client_cert` | `AMQP_TLS_CLIENT_CERT` | — | Upstream client certificate (mTLS) |
| `tls.client_key` | `AMQP_TLS_CLIENT_KEY` | — | Upstream client key (mTLS) |
| `tls.skip_verify` | `AMQP_TLS_SKIP_VERIFY` | `false` | Skip upstream TLS verification |

### Example config.toml

```toml
[listen]
port = 5673

[upstream]
url = "amqps://rabbitmq.internal:5671"

[pool]
max_channels = 65535
idle_timeout = 30
max_client_connections = 500

[tls]
ca_cert = "/etc/ssl/certs/rabbitmq-ca.pem"
client_cert = "/etc/ssl/certs/amqplex-client.pem"
client_key  = "/etc/ssl/private/amqplex-client-key.pem"
```

## Metrics

Available at `http://<host>:<port+1>/metrics` in Prometheus text format.

| Metric | Type | Description |
|--------|------|-------------|
| `amqplex_active_clients` | gauge | Current connected clients |
| `amqplex_upstream_connections` | gauge | Total upstream AMQP connections |
| `amqplex_upstream_reconnecting` | gauge | Upstreams currently reconnecting |
| `amqplex_channels_used` | gauge | AMQP channels currently allocated |
| `amqplex_channels_pending_close` | gauge | Channels awaiting CloseOk from broker |
| `amqplex_upstream_reconnect_attempts_total` | counter | Cumulative reconnect attempts |

## License

MIT — see [LICENSE](LICENSE).

## Development

```bash
# Start RabbitMQ
make docker-up

# Run unit tests
make test

# Run integration tests (requires Docker)
make integration

# Build release binaries (linux/darwin × amd64/arm64)
make release

# Stop RabbitMQ
make docker-down
```
