.PHONY: build test clean run docker-up docker-down

build:
	go build -o bin/amqproxy ./main.go

test:
	go test -v ./...

test-short:
	go test -short -v ./...

integration-test:
	go test -v ./tests/...

clean:
	rm -rf bin/ test_certs/

run:
	go run ./main.go

docker-up:
	docker-compose up --build

docker-down:
	docker-compose down
