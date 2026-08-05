COMPOSE := docker compose -f deploy/docker-compose.yml
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
E2E_ENV := PGMUX_TEST_PG_HOST=localhost PGMUX_TEST_PG_SSLMODE=verify-full

.PHONY: build run test race e2e fuzz lint certs compose-up compose-down docker clients bench dashboard clean

build:
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/pgmux ./cmd/pgmux

run: build
	./bin/pgmux -config pgmux.yaml

test:
	go test ./internal/...

race:
	go test -race ./internal/...

# e2e needs the Compose Postgres: run `make compose-up` first.
e2e:
	$(E2E_ENV) go test -race -count=1 -timeout 10m ./test/e2e/...

fuzz:
	go test ./internal/protocol -run '^$$' -fuzz '^FuzzReadMessage$$' -fuzztime 30s
	go test ./internal/prepared -run '^$$' -fuzz '^FuzzRename$$' -fuzztime 30s
	go test ./internal/auth -run '^$$' -fuzz '^FuzzScramServer$$' -fuzztime 30s
	go test ./internal/auth -run '^$$' -fuzz '^FuzzParseUserlist$$' -fuzztime 30s

lint:
	golangci-lint run ./...

certs:
	deploy/certs/gen.sh

compose-up:
	@test -f deploy/certs/server.crt || deploy/certs/gen.sh
	$(COMPOSE) up -d --wait postgres

compose-down:
	$(COMPOSE) --profile pgmux --profile bench down -v

docker:
	docker build -f deploy/Dockerfile --build-arg VERSION=$(VERSION) -t pgmux:$(VERSION) .

# Runs every client suite against a pgmux that listens on localhost:6432.
clients:
	cd clients/go && go test -count=1 ./...
	cd clients/python && python3 -m pytest -q
	cd clients/node && npm test
	cd clients/java && mvn -q test

bench:
	bench/run.sh

dashboard:
	cd dashboard && npm ci && npm run build

clean:
	rm -rf bin dashboard/dist
