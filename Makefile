.DEFAULT_GOAL := build
.PHONY: build test install redeploy check lint fmt vet cross check-head ci-pins pins

# Pins (ADR 1): every go target builds against the sock-contract tag in
# .contract-ref, the tag CI checks out, not whatever the sibling checkout
# has at HEAD. make pins clones the tag once under PIN_CACHE, from the
# sibling checkout (fetching the tag when it lacks it), then reuses it
# offline, and writes .pins/go.work, whose replace line overrides go.mod's
# ../contract with that clone. go.mod itself is never edited.
# make check-head (PINS empty) builds against the sibling's HEAD instead.
PIN_CACHE ?= $(or $(XDG_CACHE_HOME),$(HOME)/.cache)/sock-pins
PINNED := contract
PINS ?= $(PINNED)
ifneq ($(PINS),)
export GOWORK := $(CURDIR)/.pins/go.work
endif

# Never touches the sibling checkout's HEAD, index or working tree: a tag the
# sibling lacks is fetched as a ref, and each clone is made in a temp dir and
# renamed into place, so concurrent runs share one clone and never see half
# of one.
pins:
ifneq ($(PINS),)
	@set -e; mkdir -p .pins; work=.pins/go.work.$$$$; \
	printf 'go %s\n\nuse ..\n\n' "$$(awk '$$1 == "go" {print $$2}' go.mod)" > $$work; \
	for m in $(PINS); do \
		tag=$$(cat .$$m-ref); dir="$(PIN_CACHE)/$$m/$$tag"; \
		if [ ! -d "$$dir" ]; then \
			git -C ../$$m rev-parse -q --verify "refs/tags/$$tag" >/dev/null || git -C ../$$m fetch -q origin tag "$$tag"; \
			mkdir -p "$(PIN_CACHE)/$$m"; tmp=$$(mktemp -d "$(PIN_CACHE)/$$m/.tmp.XXXXXX"); \
			git clone -q --no-checkout ../$$m "$$tmp"; \
			git -C "$$tmp" -c advice.detachedHead=false checkout -q "$$tag^{commit}"; \
			perl -e 'rename($$ARGV[0], $$ARGV[1]) or -d $$ARGV[1] or die "$$ARGV[1]: $$!\n"' "$$tmp" "$$dir"; \
			rm -rf "$$tmp"; \
		fi; \
		echo "replace github.com/guygrigsby/sock-$$m => $$dir" >> $$work; \
	done; \
	mv $$work .pins/go.work
endif

build test vet cross lint: pins

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

# ci-pins fails unless every sibling checkout in ci.yml takes its ref from the
# ref file make check builds against, so CI and make check cannot pin
# different tags.
ci-pins:
	@for m in $(PINNED); do \
		grep -q "$$m=\$$(cat [^)]*\.$$m-ref)\" >> \"\$$GITHUB_OUTPUT\"" .github/workflows/ci.yml && \
		grep -q "ref: \$${{ steps.pins.outputs.$$m }}" .github/workflows/ci.yml || \
		{ echo "ci.yml must check out sock-$$m at the tag in .$$m-ref (steps.pins.outputs.$$m)" >&2; exit 1; }; \
	done
	@! grep -Eq '^ *ref: [^$$]' .github/workflows/ci.yml || { echo 'ci.yml hardcodes a ref; read it from a ref file' >&2; exit 1; }

check: ci-pins fmt vet cross lint test

check-head:
	$(MAKE) check PINS=
