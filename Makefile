.PHONY: build test acceptance integration clean install

GO := CGO_ENABLED=0 go
BINARY := claudit
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X github.com/DanielJonesEB/claudit/cmd.version=$(VERSION)"

build:
	$(GO) build $(LDFLAGS) -o $(BINARY) .

test:
	$(GO) test ./internal/...

acceptance: build
	$(GO) test ./tests/acceptance/... -v

# Integration test with real Claude Code CLI
# Requires: ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN environment variable
# Requires: Claude Code CLI in PATH
# Opt out with: SKIP_CLAUDE_INTEGRATION=1
integration: build
	CLAUDIT_BINARY=$(PWD)/$(BINARY) $(GO) test ./tests/integration/... -v -timeout 120s

all: test acceptance

clean:
	rm -f $(BINARY)

install: build
	cp $(BINARY) $(GOPATH)/bin/

# Development helpers
.PHONY: fmt lint

fmt:
	go fmt ./...

lint:
	go vet ./...
