.PHONY: build build-docker test install clean

PREFIX ?= $(HOME)/.local
BIN := $(PREFIX)/bin/scriber

# Build with locally-installed Go. Requires Go 1.23+ and gcc (for malgo CGO).
build:
	CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o ./dist/scriber ./cmd/scriber

# Build inside Docker — no host Go/gcc needed. Output to ./dist/scriber.
build-docker:
	mkdir -p ./dist
	docker build -f Dockerfile.build -t scriber-client-build .
	docker run --rm -v $(CURDIR)/dist:/out scriber-client-build

test:
	go test ./...

install: build
	install -Dm755 ./dist/scriber $(BIN)
	@echo "installed: $(BIN)"

clean:
	rm -rf ./dist
