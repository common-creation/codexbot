.PHONY: build test test-go test-web web-build compose-config fmt

build: web-build
	go build ./cmd/...

test: test-go test-web

test-go:
	go test ./...

test-web:
	cd web && npm test -- --run

web-build:
	cd web && npm run build

compose-config:
	docker compose --env-file deploy/.env.example -f deploy/compose.yaml config --quiet

fmt:
	gofmt -w cmd internal
