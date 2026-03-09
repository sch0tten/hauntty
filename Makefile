VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  = -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(DATE)

.PHONY: build build-all clean test

build:
	go build -ldflags "$(LDFLAGS)" -o hauntty .

build-all: build
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w $(LDFLAGS)" -o hauntty-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w $(LDFLAGS)" -o hauntty-linux-arm64 .

clean:
	rm -f hauntty hauntty-linux-amd64 hauntty-linux-arm64

test:
	go test ./...
