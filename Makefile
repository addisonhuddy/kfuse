.PHONY: all build test test-race test-fuse test-install test-preflight test-local-network integration-test local-up local-demo local-down vet lint fmt fmt-check docker-build release-snapshot release-verify vuln licenses install-tools clean

BIN := kfuse
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE ?= addisonhuddy/kfuse

# Vendored license texts/sources under third_party/ are kept verbatim.
GOFILES := $(shell find . -name '*.go' -not -path './third_party/*')

# Release tooling pins; keep GOVULNCHECK_VERSION in sync with
# .github/workflows/vuln.yml.
GOVULNCHECK_VERSION := v1.7.0
GO_LICENSES_VERSION := v2.0.1

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

test-install:
	bash scripts/install_test.sh

test-preflight:
	bash scripts/preflight_test.sh

test-local-network:
	bash scripts/local_network_test.sh

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
	gofmt -w $(GOFILES)

fmt-check:
	@out=$$(gofmt -l $(GOFILES)); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# Install the pinned release tools used by vuln/licenses.
install-tools:
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	go install github.com/google/go-licenses/v2@$(GO_LICENSES_VERSION)

# Dependency vulnerability audit (govulncheck; `make install-tools` first).
vuln:
	govulncheck ./...

# Collect dependency license/notice texts into third_party/ (committed, and
# shipped in release archives and the runtime image). go-licenses locates
# the Go stdlib through the GOROOT its own binary was built with, so point
# it at the active toolchain.
licenses:
	GOROOT=$$(go env GOROOT) go-licenses save ./... --save_path third_party --force --ignore github.com/addisonhuddy/kfuse

# Local release-candidate check: snapshot build, checksums, version output,
# advertised arches, and bundled license files. Publishes nothing.
release-verify:
	bash scripts/verify_release.sh

clean:
	rm -f $(BIN)
