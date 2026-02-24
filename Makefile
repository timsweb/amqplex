.PHONY: build test clean run docker-up docker-down integration

build:
	go build -o bin/amqproxy ./main.go

test:
	go test -v ./...

test-short:
	go test -short -v ./...

integration-test:
	go test -v ./tests/...

integration:
	docker compose -f docker-compose.test.yml up -d
	go test -tags integration -v -timeout 120s ./tests/ ; docker compose -f docker-compose.test.yml down

clean:
	rm -rf bin/ test_certs/

run:
	go run ./main.go

docker-up:
	docker-compose up --build

docker-down:
	docker-compose down
