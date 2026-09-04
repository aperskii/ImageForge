# The API binary, built static and shipped on a distroless base.
#
# cmd/api never reaches internal/adapters/imageproc, so it needs neither cgo nor
# libvips. That is worth keeping true: it lets this image be a single static
# binary with no shell, no package manager and nothing else to patch.
#
# Build from the repository root:
#   docker build -f deployments/docker/api.Dockerfile -t imageforge-api .

# ---------------------------------------------------------------- build -----
FROM golang:1.25-bookworm AS build

WORKDIR /src

# Dependencies are their own layer, so editing source does not re-download the
# module cache on every build.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG VERSION=dev
# CGO_ENABLED=0 is what makes the result static, and therefore runnable on a
# base with no libc at all. -trimpath keeps build paths out of the binary, and
# -s -w drop the symbol and DWARF tables it has no use for in production.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/api ./cmd/api

# A writable data directory, created here because the runtime stage has no
# shell to make one with. The memory backend writes uploads to it; the AWS
# backend never touches it.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

# --------------------------------------------------------------- runtime ----
# distroless/static carries CA certificates, /etc/passwd and timezone data, and
# nothing else. No shell means nothing for an attacker who finds a way to
# execute to actually run.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

COPY --from=build /out/api /usr/local/bin/api
COPY --from=build --chown=65532:65532 /out/data /var/lib/imageforge

ENV IMAGEFORGE_STORAGE_DIR=/var/lib/imageforge/storage

# Runs as uid 65532, which the base image already defines. Nothing in the image
# is writable by it.
USER nonroot:nonroot

EXPOSE 8080

# The binary probes itself, because there is no curl here to do it.
HEALTHCHECK --interval=10s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/usr/local/bin/api", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/api"]
