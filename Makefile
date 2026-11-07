# Build ferry for this machine, install it where `ferry remote` is expected
# (~/.local/bin/ferry), or cross-build for the other machine.
BIN := $(HOME)/.local/bin

.PHONY: build install dist test

build:
	go build -o ferry .

install: build
	mkdir -p $(BIN)
	install -m 0755 ferry $(BIN)/ferry

dist:
	mkdir -p dist
	GOOS=darwin GOARCH=arm64 go build -o dist/ferry-darwin-arm64 .
	GOOS=darwin GOARCH=amd64 go build -o dist/ferry-darwin-amd64 .
	GOOS=linux GOARCH=amd64 go build -o dist/ferry-linux-amd64 .

test:
	go test ./...
