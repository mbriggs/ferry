# Build ferry for this machine, install it where `ferry remote` is expected
# (~/.local/bin/ferry), or cross-build for the other machine.
BIN := $(HOME)/.local/bin
# This Makefile's checkout, so `make -f <lane>/Makefile install` from elsewhere still guards the lane.
ROOT := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build install dist push test fuzz

build:
	$(GOBUILD) -o ferry .

install: build
	$(ROOT)/bin/worktree guard --confirm "make install"
	mkdir -p $(BIN)
	install -m 0755 ferry $(BIN)/ferry
	mkdir -p $(HOME)/.claude/skills/ferry
	install -m 0644 skills/ferry/SKILL.md $(HOME)/.claude/skills/ferry/SKILL.md
	install -m 0755 skills/ferry/after-exit.sh $(HOME)/.claude/skills/ferry/after-exit.sh

dist:
	mkdir -p dist
	GOOS=darwin GOARCH=arm64 $(GOBUILD) -o dist/ferry-darwin-arm64 .
	GOOS=darwin GOARCH=amd64 $(GOBUILD) -o dist/ferry-darwin-amd64 .
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o dist/ferry-linux-amd64 .
	GOOS=linux GOARCH=arm64 $(GOBUILD) -o dist/ferry-linux-arm64 .
	cd dist && shasum -a 256 ferry-* > SHA256SUMS

# Rebuild for every platform and put this build on each host in
# ~/.config/ferry/hosts that runs a different one.
push: build dist
	./ferry push

test:
	go test ./...

# Run each fuzz target in turn for FUZZTIME. `go test` alone only replays the
# saved inputs in testdata/fuzz; this looks for new ones. A failing input is
# written there: commit it with the fix so `go test` keeps checking it.
FUZZTIME ?= 30s
fuzz:
	@for t in $$(grep -ho '^func Fuzz[A-Za-z0-9_]*' *_test.go | cut -c6-); do \
		echo "== $$t"; \
		go test -run '^$$' -fuzz "^$$t$$" -fuzztime $(FUZZTIME) . || exit 1; \
	done
