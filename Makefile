.PHONY: all build test test-race test-fuse integration-test local-up local-demo local-down vet lint fmt fmt-check probe docker-build release-snapshot clean

BIN := kfuse
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE ?= addisonhuddy/kfuse

all: fmt-check vet lint build test

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/kfuse

# Release image from source (see Dockerfile). Native arch only; use buildx
# with --platform linux/amd64,linux/arm64 for multi-arch.
docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):dev .

# Local dry run of the release: archives + checksums in dist/, no publish.
release-snapshot:
	goreleaser release --snapshot --clean

# Unit tests: no credentials needed.
test:
	go test ./...

# Unit tests under the race detector; the commit/checkpoint pipeline tests
# in internal/session rely on it to catch ordering regressions.
test-race:
	go test -race ./...

# Real FUSE mount tests (Linux, /dev/fuse + fusermount3). Elsewhere these
# skip; here a missing mount facility fails the run instead.
test-fuse:
	KFUSE_REQUIRE_FUSE=1 go test -race -count=1 -run 'RealMount' ./internal/daemon/

# Integration tests: need a repo-root .env with a reachable Kafka broker and
# S3-compatible bucket (hosted or examples/local).
integration-test:
	go test -tags integration ./...

# Local development stack: Apache Kafka in single-node KRaft mode + MinIO.
local-up:
	./examples/local/up.sh

local-demo:
	./examples/local/demo.sh

local-down:
	./examples/local/down.sh

vet:
	go vet ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# Cheapest end-to-end check: FUSE + S3 probe, no Kafka append.
probe:
	./examples/local/run.sh --probe

clean:
	rm -f $(BIN)
