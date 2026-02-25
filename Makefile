.PHONY: build release test clean run docker-up docker-down integration

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

clean:
	rm -rf bin/ dist/ test_certs/

run:
	go run ./main.go

docker-up:
	docker-compose up --build

docker-down:
	docker-compose down
