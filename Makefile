BINARY  := traust-cli
VERSION ?= $(shell cat VERSION 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/traust-security/traust-cli/cmd.version=$(VERSION)

.PHONY: build test vet lint fmt tidy check clean check-version

build:
	go build -buildvcs=false -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/traust-cli

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

check: build vet test check-version

check-version:
	@printf '%s' "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { \
		printf 'VERSION must be X.Y.Z, got: %s\n' "$(VERSION)"; exit 1; }
	@grep -qx "CLI_VERSION=$(VERSION)" version.args || { \
		printf 'version.args must contain exactly CLI_VERSION=%s\n' "$(VERSION)"; exit 1; }

clean:
	rm -rf bin/
