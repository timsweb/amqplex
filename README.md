# AMQProxy

High-performance AMQP proxy with connection/channel pooling and runtime certificate loading.

## Features
- Connection pooling per credential set
- Safe channel reuse tracking
- Multiplexing (multiple clients per upstream connection)
- Runtime certificate loading (OS store, custom files, env vars)
- Optional mTLS support
- Health check endpoint

## Usage

```bash
# Build
make build

# Run with config
./bin/amqproxy --config config.toml

# Run with defaults
./bin/amqproxy amqps://rabbitmq:5671
```

## Development

```bash
# Start RabbitMQ
make docker-up

# Run tests
make test

# Stop RabbitMQ
make docker-down
```
