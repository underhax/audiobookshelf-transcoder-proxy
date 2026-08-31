VERSION ?= dev

.PHONY: check test coverage vulncheck trivy docker-lint verify build

check:
	@test -z "$$(gofmt -s -l .)" || (echo "Unformatted files found. Run 'gofmt -s -w .' to fix them." && false)
	golangci-lint run ./...
	go build ./...

test:
	go test -v -race ./...

coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

vulncheck:
	govulncheck ./...

trivy:
	trivy fs --severity CRITICAL,HIGH .

docker-lint:
	hadolint docker/Dockerfile

verify: check test vulncheck trivy docker-lint

build:
	go build -ldflags "-s -w -X main.Version=$(VERSION)" -o abstp ./cmd/abstp
