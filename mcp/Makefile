.PHONY: build test vet lint run clean

BINARY ?= kuso-mcp
PKG    ?= ./...

build:
	go build -o bin/$(BINARY) .

test:
	go test $(PKG) -count=1

vet:
	go vet $(PKG)

run: build
	./bin/$(BINARY)

clean:
	rm -rf bin
