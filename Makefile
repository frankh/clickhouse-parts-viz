.PHONY: build test run demo demo-down

build:
	go build -o bin/partsviz ./cmd/partsviz

test:
	go vet ./...
	go test ./...

run:
	go run ./cmd/partsviz

demo:
	docker compose -f deploy/docker-compose.yml up --build -d

demo-down:
	docker compose -f deploy/docker-compose.yml down -v
