VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PREFIX  ?= $(HOME)/.local

.PHONY: build test vet fmt install clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o agentctl ./cmd/agentctl

test:
	go test ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run: make fmt" && exit 1)

fmt:
	gofmt -w .

install: build
	install -d $(PREFIX)/bin
	install -m 0755 agentctl $(PREFIX)/bin/agentctl

clean:
	rm -f agentctl
