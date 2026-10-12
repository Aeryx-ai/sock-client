.DEFAULT_GOAL := build
.PHONY: build test install redeploy check lint fmt vet

build:
	go build ./...

test:
	go test -race ./...

install:
	@echo "sock-client is a library; nothing to install"

redeploy:
	@echo "sock-client is a library; nothing to deploy"

lint:
	golangci-lint run

fmt:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

check: fmt vet lint test
