.DEFAULT_GOAL := build
.PHONY: build test install redeploy check lint fmt vet cross

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

# cross vets every platform sock ships to, so a unix-only call fails here
cross:
	@for os in darwin linux windows; do GOOS=$$os go vet ./... || exit 1; done

check: fmt vet cross lint test
