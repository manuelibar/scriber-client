.PHONY: build build-docker test install clean

PREFIX ?= $(HOME)/.local
BIN := $(PREFIX)/bin/stt

# Build with locally-installed Go. Requires Go 1.23+ and gcc (for malgo CGO).
build:
	CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o ./dist/stt ./cmd/scriber

# Build inside Docker — no host Go/gcc needed. Output to ./dist/stt.
build-docker:
	mkdir -p ./dist
	docker build -f Dockerfile.build -t stt-client-build .
	docker run --rm -v $(CURDIR)/dist:/out stt-client-build

test:
	go test ./...

install: build
	install -Dm755 ./dist/stt $(BIN)
	@echo "installed: $(BIN)"

clean:
	rm -rf ./dist
