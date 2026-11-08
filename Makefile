# Build ferry for this machine, install it where `ferry remote` is expected
# (~/.local/bin/ferry), or cross-build for the other machine.
BIN := $(HOME)/.local/bin
# This Makefile's checkout, so `make -f <lane>/Makefile install` from elsewhere still guards the lane.
ROOT := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build install dist test

build:
	$(GOBUILD) -o ferry .

install: build
	$(ROOT)/bin/worktree guard --confirm "make install"
	mkdir -p $(BIN)
	install -m 0755 ferry $(BIN)/ferry

dist:
	mkdir -p dist
	GOOS=darwin GOARCH=arm64 $(GOBUILD) -o dist/ferry-darwin-arm64 .
	GOOS=darwin GOARCH=amd64 $(GOBUILD) -o dist/ferry-darwin-amd64 .
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o dist/ferry-linux-amd64 .
	GOOS=linux GOARCH=arm64 $(GOBUILD) -o dist/ferry-linux-arm64 .
	cd dist && shasum -a 256 ferry-* > SHA256SUMS

test:
	go test ./...
