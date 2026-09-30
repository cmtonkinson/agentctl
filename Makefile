.PHONY: build test vet fmt clean help dist deploy patch minor major

build:
	go build -trimpath -o agentctl ./cmd/agentctl

test:
	go test ./...
	./scripts/test-release.sh

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run: make fmt" && exit 1)

fmt:
	gofmt -w .

dist:
	@tag=$$(git describe --tags --exact-match HEAD) && ./scripts/build-release.sh "$$tag"

BUMP := $(filter patch minor major,$(MAKECMDGOALS))
deploy:
ifeq ($(words $(BUMP)),1)
	./scripts/cut-release.sh $(BUMP)
else
	@echo 'usage: make deploy <patch|minor|major>' >&2
	@exit 64
endif

patch minor major:
	@:

clean:
	rm -f agentctl

help:
	@printf '%s\n' \
		'Targets:' \
		'  build    Build ./agentctl (default)' \
		'  test     Run Go and release workflow tests' \
		'  vet      Run go vet and check gofmt' \
		'  fmt      Format Go source files' \
		'  dist     Build release archives from the tag checked out at HEAD' \
		'  deploy   Verify main CI, then tag and push (make deploy <patch|minor|major>)' \
		'  clean    Remove the local ./agentctl binary' \
		'  help     Show these Makefile targets'
