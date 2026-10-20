BINARY  := traust-cli
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/traust-security/traust-cli/cmd.version=$(VERSION)

.PHONY: build test vet lint fmt tidy check clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/traust-cli

test:
	go test -race ./...

vet:
	go vet ./...

lint: vet
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

fmt:
	go fmt ./...

tidy:
	go mod tidy

check: build vet test

clean:
	rm -rf bin/
