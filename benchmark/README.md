# AMQProxy Benchmark Suite

This benchmark suite compares AMQplex and cloudamqp/amqproxy performance.

## Quick Start

```bash
# Run all benchmarks (starts infrastructure, runs benchmarks, tears down)
make benchmark-all

# View comparison of saved results
make benchmark-compare
```

## Detailed Usage

### Running Specific Benchmarks

```bash
# Setup infrastructure only
make benchmark-setup

# Run specific scenarios
make benchmark-short-lived
make benchmark-high-concurrency
make benchmark-mixed-sizes

# Cleanup when done
make benchmark-teardown
```

### Understanding Results

Results are saved to `benchmark/results/` in JSON format. Each result includes:
- **messages**: Total messages sent
- **duration**: Total benchmark duration
- **throughput_msg_per_sec**: Messages per second
- **cpu.cpu_percent**: Average CPU usage of proxy container
- **memory.current_rss_kb**: Memory usage in KB

### Scenarios

1. **Short-lived connections**: One connection opened and closed per message — models PHP-style workloads where the proxy's connection pooling provides the most benefit.
2. **High concurrency**: 10× parallelism factor, each goroutine opens its own connection. Tests how well each proxy handles concurrent connection load.
3. **Mixed message sizes**: Small (1KB), medium (10KB), large (100KB) — shows throughput impact of frame size.

### Troubleshooting

If benchmarks fail:
1. Ensure Docker is running: `docker ps`
2. Check services are healthy: `docker compose -f benchmark/docker-compose.benchmark.yml ps`
3. View logs: `docker compose -f benchmark/docker-compose.benchmark.yml logs <service>`
