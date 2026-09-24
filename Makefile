VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PREFIX  ?= $(HOME)

.PHONY: build test vet fmt install clean help

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o agentctl ./cmd/agentctl

test:
	go test ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run: make fmt" && exit 1)

fmt:
	gofmt -w .

install:
	install -d $(PREFIX)/bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(PREFIX)/bin/agentctl ./cmd/agentctl

clean:
	rm -f agentctl

help:
	@printf '%s\n' \
		'Targets:' \
		'  build    Build ./agentctl (default)' \
		'  test     Run Go tests' \
		'  vet      Run go vet and check gofmt' \
		'  fmt      Format Go source files' \
		'  install  Build and install to $(PREFIX)/bin/agentctl' \
		'  clean    Remove the local ./agentctl binary' \
		'  help     Show these Makefile targets' \
		'' \
		'Variables:' \
		'  PREFIX   Install prefix (default: $(HOME))' \
		'  VERSION  Version embedded in the binary (default: git describe)'
