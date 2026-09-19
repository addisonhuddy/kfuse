# kfuse release image, built from source.
#
#   docker build -t kfuse --build-arg VERSION=$(git describe --tags --always) .
#   docker run --rm --privileged --device /dev/fuse --env-file .env \
#     -v /path/to/base:/work/lower kfuse mount --foreground
#
# The container needs --privileged and /dev/fuse because kfuse mounts FUSE
# inside it. Multi-arch (linux/amd64, linux/arm64) via buildx:
#   docker buildx build --platform linux/amd64,linux/arm64 -t kfuse .
#
# GoReleaser uses Dockerfile.goreleaser instead, which copies its prebuilt
# binary into the same runtime stage.
# Builder tag tracks the `go` directive in go.mod.
FROM --platform=$BUILDPLATFORM golang:1.26.8@sha256:6c2a5538f964f1c82f97ad14988bf05de100d922d159d0e398b54c7b0ca0c6c9 AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY internal/ internal/
COPY cmd/kfuse/ cmd/kfuse/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kfuse ./cmd/kfuse

FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251
RUN apt-get update \
 && apt-get install -y --no-install-recommends fuse3 ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/kfuse /usr/local/bin/kfuse
RUN mkdir -p /work/lower /var/lib/kfuse
WORKDIR /work/lower
ENV KF_STATE_DIR=/var/lib/kfuse
ENTRYPOINT ["/usr/local/bin/kfuse"]
CMD ["--help"]
