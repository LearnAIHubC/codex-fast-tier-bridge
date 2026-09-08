VERSION ?= 1.4.0
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

ifeq ($(GOOS),darwin)
EXT := dylib
else ifeq ($(GOOS),windows)
EXT := dll
else
EXT := so
endif

.PHONY: test build clean

test:
	go test ./...

build:
	mkdir -p dist/$(GOOS)/$(GOARCH)
	go build -buildvcs=false -buildmode=c-shared -trimpath \
		-o dist/$(GOOS)/$(GOARCH)/codex-fast-tier-bridge-v$(VERSION).$(EXT) .

clean:
	rm -rf dist
