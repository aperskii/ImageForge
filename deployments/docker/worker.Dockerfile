# The worker binary, linked against libvips through cgo.
#
# Unlike the API this cannot be a static binary on a scratch base: govips binds
# libvips, so the build needs its headers and the runtime needs the shared
# library. The two stages differ accordingly -- libvips-dev to compile against,
# libvips42 to run against -- which keeps the compiler, the headers and the
# whole Go toolchain out of the shipped image.
#
# Build from the repository root:
#   docker build -f deployments/docker/worker.Dockerfile -t imageforge-worker .

# ---------------------------------------------------------------- build -----
FROM golang:1.25-bookworm AS build

# libvips-dev brings the headers and the pkg-config file cgo needs to compile
# govips; pkg-config itself is what finds them.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        libvips-dev \
        pkg-config \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG VERSION=dev
# CGO_ENABLED=1 is the point of this image. The binary is dynamically linked
# against libvips and its own dependency tree, so the runtime stage has to
# provide them.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/worker ./cmd/worker

# --------------------------------------------------------------- runtime ----
FROM debian:bookworm-slim AS runtime

# libvips42 is the runtime library only: no headers, no tools, no docs.
# ca-certificates is needed to speak TLS to S3, SQS and DynamoDB.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        libvips42 \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 65532 --user-group --no-create-home --shell /usr/sbin/nologin imageforge

RUN mkdir -p /var/lib/imageforge && chown imageforge:imageforge /var/lib/imageforge

COPY --from=build /out/worker /usr/local/bin/worker

# The memory backend writes uploads here; the AWS backend never touches it.
ENV IMAGEFORGE_STORAGE_DIR=/var/lib/imageforge/storage

USER imageforge:imageforge

# The metrics and health endpoint, not an application port.
EXPOSE 9090

HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/worker", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/worker"]
