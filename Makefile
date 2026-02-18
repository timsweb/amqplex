.PHONY: build test clean docker-up docker-down

build:
	go build -o bin/amqproxy main.go

test:
	go test -v ./...

clean:
	rm -rf bin/

docker-up:
	docker-compose up -d

docker-down:
	docker-compose down
