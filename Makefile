.PHONY: dev setup test test-unit test-integration clean build lint

dev: setup
	set -a && . ./.env.local && set +a && go run ./cmd/gateway/

setup:
	./scripts/setup-local.sh

test: test-unit test-integration

test-unit:
	go test -short -v ./...

test-integration: setup
	set -a && . ./.env.local && set +a && go test -v -count=1 ./internal/...

clean:
	./scripts/teardown-local.sh

build:
	go build -o bin/gateway ./cmd/gateway/
	go build -o bin/worker ./cmd/worker/

lint:
	go vet ./...
