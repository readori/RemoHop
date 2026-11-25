# RemoHop build entry points. Release binaries are pure Go (CGO_ENABLED=0),
# static, and cross-compile from any host.

GO             ?= go
BIN            := bin/remohop
DIST           := dist
VERSION        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS        := -s -w -X github.com/readori/RemoHop/internal/cli.Version=$(VERSION)
CROSS_TARGETS  ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: all build test race vet fmt-check cross quick clean mobile-test

all: fmt-check vet build test

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/remohop

test:
	$(GO) test ./...

# The race detector needs cgo; everything else stays CGO-free.
race:
	CGO_ENABLED=1 $(GO) test -race ./...

# Every OS the release ships: build tags make each one compile different files.
vet:
	$(GO) vet ./...
	CGO_ENABLED=0 GOOS=darwin $(GO) vet ./...
	CGO_ENABLED=0 GOOS=windows $(GO) vet ./...

# The mobile library is a separate module (mobile/go.mod).
mobile-test:
	cd mobile && $(GO) vet ./... && $(GO) test ./...

# gofmt -l prints files whose formatting differs; any output fails the target.
fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needs to be run on:"; echo "$$out"; exit 1; fi

# Each entry is GOOS/GOARCH. CI builds one entry per job, e.g.
# make cross CROSS_TARGETS=linux/arm64
cross:
	@mkdir -p $(DIST)
	@for t in $(CROSS_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; \
		if [ "$$os" = windows ]; then ext=.exe; fi; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" \
			-o $(DIST)/remohop-$$os-$$arch$$ext ./cmd/remohop || exit 1; \
	done

# The double-click builds of `remohop quick`: RemoHop.exe (Windows GUI program with
# icon and manifest), RemoHop.app (macOS, universal, unsigned) and a Linux
# binary, in dist/quick. See packaging/quick/README.md.
quick:
	VERSION="$(VERSION)" GO="$(GO)" sh packaging/quick/build.sh

clean:
	rm -rf $(BIN) $(DIST)
