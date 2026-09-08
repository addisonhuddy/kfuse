# kfuse best-of-n image: kfuse + toy project baked in, fuse3 runtime.
# Build:  docker build -f examples/best-of-n/Dockerfile -t kfuse-best-of-n .
# Run:    docker run --rm --privileged --device /dev/fuse \
#           --env-file <creds-file> kfuse-best-of-n [parent|hypo|judge|bash]
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY internal/ internal/
COPY cmd/ cmd/
RUN CGO_ENABLED=0 go build -o /out/kfuse ./cmd/kfuse

FROM ubuntu:24.04
RUN apt-get update \
 && apt-get install -y --no-install-recommends fuse3 ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/kfuse /usr/local/bin/kfuse
COPY examples/best-of-n/lower/ /work/lower/
COPY examples/best-of-n/demo.sh /demo.sh
RUN chmod +x /demo.sh /work/lower/parse.sh /work/lower/test.sh
WORKDIR /work/lower
ENV KF_STATE_DIR=/var/lib/kfuse
ENTRYPOINT ["/demo.sh"]
CMD ["bash"]
