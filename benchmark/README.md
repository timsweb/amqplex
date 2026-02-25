# AMQProxy Benchmark Suite

This benchmark suite compares AMQplex and cloudamqp/amqproxy performance.

## Running Benchmarks

```bash
# Start RabbitMQ and proxies
docker compose -f benchmark/docker-compose.benchmark.yml up -d

# Run all benchmarks
go test -bench=. -benchmem -benchtime=10s -run=^$ ./benchmark/scenarios/...

# Run specific benchmark scenarios
go test -bench=BenchmarkShortLived -benchtime=10s -run=^$ ./benchmark/scenarios/...
go test -bench=BenchmarkHighConcurrency -benchtime=10s -run=^$ ./benchmark/scenarios/...
go test -bench=BenchmarkMixedSizes -benchtime=10s -run=^$ ./benchmark/scenarios/...

# Stop services
docker compose -f benchmark/docker-compose.benchmark.yml down
```

## Results

Results are saved to `benchmark/results/` in JSON format for comparison.

## Scenarios

1. **Short-lived connections**: Open/close connection per message (PHP-style workload)
2. **High concurrency**: Many simultaneous client connections
3. **Mixed message sizes**: Small (1KB), medium (10KB), large (100KB) messages
