.PHONY: build release test clean run docker-up docker-down integration benchmark-setup benchmark-teardown benchmark benchmark-short-lived benchmark-high-concurrency benchmark-mixed-sizes benchmark-all benchmark-compare

build:
	go build -o bin/amqplex ./main.go

release:
	GOOS=linux  GOARCH=amd64 go build -o dist/amqplex-linux-amd64   ./main.go
	GOOS=linux  GOARCH=arm64 go build -o dist/amqplex-linux-arm64   ./main.go
	GOOS=darwin GOARCH=amd64 go build -o dist/amqplex-darwin-amd64  ./main.go
	GOOS=darwin GOARCH=arm64 go build -o dist/amqplex-darwin-arm64  ./main.go

test:
	go test -v ./...

test-short:
	go test -short -v ./...

integration-test:
	go test -v ./tests/...

integration:
	docker compose -f docker-compose.test.yml up -d
	go test -tags integration -v -timeout 120s ./tests/ ; docker compose -f docker-compose.test.yml down

benchmark-setup:
	@echo "Starting benchmark infrastructure..."
	docker compose -f benchmark/docker-compose.benchmark.yml up -d
	@echo "Waiting for services to be healthy..."
	@docker compose -f benchmark/docker-compose.benchmark.yml wait rabbitmq amqplex amqproxy || \
		docker compose -f benchmark/docker-compose.benchmark.yml ps
	@echo "Services ready."

benchmark-teardown:
	@echo "Stopping benchmark infrastructure..."
	docker compose -f benchmark/docker-compose.benchmark.yml down

benchmark: benchmark-setup
	@echo "Running benchmarks..."
	@go test -bench=. -benchmem -benchtime=10s -run=^$ -v ./benchmark/scenarios/...
	@echo "Results saved to benchmark/results/"

benchmark-short-lived: benchmark-setup
	@go test -bench=BenchmarkShortLived -benchmem -benchtime=10s -run=^$ -v ./benchmark/scenarios/...

benchmark-high-concurrency: benchmark-setup
	@go test -bench=BenchmarkHighConcurrency -benchmem -benchtime=10s -run=^$ -v ./benchmark/scenarios/...

benchmark-mixed-sizes: benchmark-setup
	@go test -bench=BenchmarkMixedSizes -benchmem -benchtime=10s -run=^$ -v ./benchmark/scenarios/...

benchmark-all: benchmark benchmark-teardown

benchmark-compare:
	@go run benchmark/scripts/compare_results.go benchmark/results

clean:
	rm -rf bin/ dist/ test_certs/

run:
	go run ./main.go

docker-up:
	docker-compose up --build

docker-down:
	docker-compose down
