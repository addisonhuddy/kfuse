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
FROM --platform=$BUILDPLATFORM golang:1.26.4 AS build
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

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends fuse3 ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/kfuse /usr/local/bin/kfuse
RUN mkdir -p /work/lower /var/lib/kfuse
WORKDIR /work/lower
ENV KF_STATE_DIR=/var/lib/kfuse
ENTRYPOINT ["/usr/local/bin/kfuse"]
CMD ["--help"]
