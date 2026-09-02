# ImageForge

![Status](https://img.shields.io/badge/status-work%20in%20progress-yellow)

ImageForge is a cloud-native image processing platform. Clients upload an image
through the HTTP API, which stores the original in object storage and enqueues a
processing job; a pool of workers then picks the job up, applies the requested
transformations (resize, crop, format conversion, optimization) and publishes the
derived assets back to storage while recording job state and metadata. The
codebase follows a hexagonal (ports and adapters) layout so that the domain and
use cases stay independent of AWS, HTTP and any other infrastructure concern,
and the whole stack can be run locally against LocalStack.

## Architecture

```
+-------------------------------------------------------------+
|                                                             |
|   ASCII ARCHITECTURE DIAGRAM PLACEHOLDER                    |
|                                                             |
|   (client -> api -> queue -> worker -> storage / metadata)  |
|                                                             |
+-------------------------------------------------------------+
```

## Layout

```
cmd/api                  HTTP API entrypoint
cmd/worker               Async processing worker entrypoint
internal/domain          Entities and value objects
internal/usecase         Application business rules
internal/ports           Interfaces consumed by the use cases
internal/adapters        Infrastructure implementations of the ports
  httpapi                HTTP transport (chi router, middleware)
  localstorage           Filesystem-backed Storage
  memqueue               In-memory Queue
  memrepo                In-memory JobRepository
  imageproc              Image processing (govips, or pure Go via -tags nogovips)
web/                     Front-end assets
deployments/terraform    Infrastructure as code
deployments/docker       Container build files
.github/workflows        CI pipelines
```

## Getting started

Requirements: Go 1.23+, Docker (with Compose), `golangci-lint`, and libvips 8.10+
(`libvips-dev` on Debian/Ubuntu, `brew install vips` on macOS). To build without
libvips, see [Image processing backends](#image-processing-backends).

```sh
make dev          # start the local AWS stack (LocalStack on :4566)
make run-api      # run the HTTP API
make run-worker   # run the worker
make build        # compile both binaries into ./bin
make test         # run the test suite with the race detector
make lint         # run golangci-lint
```



## API

The API accepts an upload, stores it, queues a job and reports on it. Nothing
drains the queue yet, so jobs stay `pending` until the worker is implemented.

| Method | Path         | Purpose                                        |
| ------ | ------------ | ---------------------------------------------- |
| `POST` | `/uploads`   | Multipart upload, returns `202` with the job   |
| `GET`  | `/jobs/{id}` | Job status, and the result URL once done       |
| `GET`  | `/healthz`   | Liveness, no dependency checks                 |
| `GET`  | `/readyz`    | Readiness                                      |

`POST /uploads` takes a `file` part carrying the image and a `spec` part
carrying the transformation as JSON:

```sh
curl -X POST http://localhost:8080/uploads \
  -F 'file=@photo.png' \
  -F 'spec={"width":320,"format":"webp","quality":80,"strip_metadata":true}'
```

```json
{
  "id": "0003fd2a28894230e91355084ed980ba",
  "status": "pending",
  "transformation": { "width": 320, "height": 0, "format": "webp", "quality": 80,
                      "watermark": false, "strip_metadata": true },
  "created_at": "2026-09-02T14:01:12.812Z",
  "updated_at": "2026-09-02T14:01:12.812Z"
}
```

Errors carry a stable code and the request id, which also appears in the
`X-Request-Id` header and in the server log:

```json
{"error":{"code":"invalid_transformation","message":"...","request_id":"..."}}
```

Every request passes through request ID, structured `log/slog` logging, panic
recovery, a 10MB body limit and CORS.

### Configuration

`make run-api` needs no configuration; every setting has a working default.

| Variable                      | Default          | Purpose                                |
| ----------------------------- | ---------------- | -------------------------------------- |
| `IMAGEFORGE_ADDR`             | `:8080`          | Listen address                         |
| `IMAGEFORGE_STORAGE_DIR`      | `.data/storage`  | Filesystem object store root           |
| `IMAGEFORGE_PUBLIC_BASE_URL`  | unset            | Prefix for a finished job's result URL |
| `IMAGEFORGE_CORS_ORIGINS`     | `*`              | Comma-separated allowed origins        |
| `IMAGEFORGE_MAX_UPLOAD_BYTES` | `10485760`       | Request body limit                     |
| `IMAGEFORGE_QUEUE_BUFFER`     | `1024`           | In-memory queue depth                  |
| `IMAGEFORGE_LOG_LEVEL`        | `INFO`           | `DEBUG`, `INFO`, `WARN`, `ERROR`       |
## Image processing backends

`internal/adapters/imageproc` implements the `ImageProcessor` port twice, with
one shared `Process` pipeline. The backend is chosen at compile time:

| Build              | Backend  | Reads                  | Writes            |
| ------------------ | -------- | ---------------------- | ----------------- |
| default            | govips   | everything libvips can | jpeg, png, webp, avif |
| `-tags nogovips`   | pure Go  | jpeg, png, gif, webp   | jpeg, png         |

The default backend binds libvips through cgo and is what production runs. The
`nogovips` build uses only `image/draw` and `golang.org/x/image`, so it needs no
system libraries and no cgo — useful on machines without libvips and for
cross-compilation. It cannot encode WebP or AVIF (no pure-Go encoder exists) and
returns `ErrUnsupportedFormat` if asked to.

```sh
go build ./...                      # govips, needs libvips + cgo
go build -tags nogovips ./...       # pure Go, no system dependencies
make test TEST_FLAGS='-tags nogovips -count=1'
```
## Status

Work in progress. The domain model, ports and use cases are in place and unit
tested. The image processing adapter is implemented on both backends, and the
HTTP API runs on in-process adapters with no external services. The
AWS adapters (S3, SQS, DynamoDB), the worker loop and the deployment
infrastructure are not implemented yet.
