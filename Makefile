APP      := tg-triage
PKG      := ./cmd/tgtriage
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -buildid= -X main.version=$(VERSION)

.PHONY: all build build-linux test vet fmt lint run clean

all: vet test build-linux

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP) $(PKG)

# Static binary for Debian x86_64: pure-Go SQLite, no CGO, no libc dependency.
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v2 \
		go build -trimpath -tags netgo,osusergo -ldflags "$(LDFLAGS)" -o bin/$(APP)-linux-amd64 $(PKG)

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

run:
	go run $(PKG) -env .env

clean:
	rm -rf bin
