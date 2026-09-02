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
internal/worker          Goroutine pool draining the job queue
internal/metrics         Prometheus collectors and the /metrics endpoint
internal/adapters        Infrastructure implementations of the ports
  awscfg                 AWS client construction from the environment
  s3storage              Storage on S3
  sqsqueue               Queue on SQS (long polling, delete-on-success)
  dynamorepo             JobRepository on DynamoDB
  httpapi                HTTP transport (chi router, middleware)
  localstorage           Filesystem-backed Storage
  memqueue               In-memory Queue
  memrepo                In-memory JobRepository
  imageproc              Image processing (govips, or pure Go via -tags nogovips)
web/                     Front-end assets
deployments/terraform    Infrastructure as code
deployments/docker       Container build files
test/integration         End-to-end tests against LocalStack
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

## Worker

`internal/worker` runs a fixed pool of goroutines, each pulling job identifiers
from the `Queue` and handing them to the `ProcessJob` use case. It owns
concurrency and lifecycle only; what processing a job means stays in the use
case.

On `SIGTERM` or `SIGINT` the pool stops taking new work and waits for what is
in flight, bounded by a timeout. Jobs already running are deliberately given a
context detached from the one being cancelled — otherwise "wait for in-flight
jobs" would abort them instead of letting them finish. If they outlast the
timeout they are cancelled and `Run` reports `ErrShutdownTimeout`.

A failing job is recorded against the job and counted; it never stops the pool.
A job that is no longer pending is skipped, which is the normal outcome of a
duplicate delivery from an at-least-once queue.

The in-memory queue lives inside one process, so a worker started on its own has
nothing to read and will idle. Pass image paths to submit jobs through the same
`CreateJob` use case the API uses, and watch the pipeline run end to end:

```sh
go run ./cmd/worker photo.png diagram.png
make run-worker                            # no arguments: idles until stopped
```

| Variable                      | Default         | Purpose                          |
| ----------------------------- | --------------- | -------------------------------- |
| `IMAGEFORGE_WORKERS`          | `4`             | Pool size                        |
| `IMAGEFORGE_SHUTDOWN_TIMEOUT` | `30s`           | Bound on draining in-flight jobs |
| `IMAGEFORGE_JOB_TIMEOUT`      | `2m`            | Bound on a single job            |
| `IMAGEFORGE_STORAGE_DIR`      | `.data/storage` | Filesystem object store root     |
| `IMAGEFORGE_WATERMARK_TEXT`   | `ImageForge`    | Watermark overlay text           |
| `IMAGEFORGE_SEED_FORMAT`      | `jpeg`          | Output format for seeded jobs    |
| `IMAGEFORGE_SEED_WIDTH`       | `640`           | Output width for seeded jobs     |
| `IMAGEFORGE_SEED_QUALITY`     | `80`            | Output quality for seeded jobs   |


### Observability

The worker serves Prometheus metrics on its own listener, separate from any
application port so it can be firewalled off independently.

| Metric                                    | Type      | Labels   |
| ----------------------------------------- | --------- | -------- |
| `imageforge_jobs_processed_total`         | counter   | `status` |
| `imageforge_processing_duration_seconds`  | histogram | `status` |
| `imageforge_queue_depth`                  | gauge     | —        |
| `imageforge_queue_receive_errors_total`   | counter   | —        |

`status` is `processed`, `failed` or `skipped`. All three are published at zero
from startup, so an alert on `rate(...)` is evaluable before the first failure
rather than silently matching nothing. The duration buckets span 10ms to 60s,
which is the range image work actually covers.

`queue_depth` is refreshed on a timer from the queue itself, and is advisory:
SQS reports an approximation, and it is stale by the time it is read.

```sh
curl localhost:9090/metrics
```

### Reading from the queue

A failed receive is retried with exponential backoff and full jitter: the delay
doubles per consecutive failure up to a ceiling, and the actual wait is drawn
from `[0, delay)` so a fleet of workers that lost the queue together does not
come back in lockstep and stampede it. One success resets the sequence. The
consumer never gives up, because a queue coming back should find a worker still
waiting for it.

| Variable                          | Default | Purpose                        |
| --------------------------------- | ------- | ------------------------------ |
| `IMAGEFORGE_METRICS_ADDR`         | `:9090` | Metrics listener               |
| `IMAGEFORGE_QUEUE_DEPTH_INTERVAL` | `15s`   | How often the gauge is refreshed |
## Backends

`IMAGEFORGE_BACKEND` selects which adapters the binaries wire up. Both satisfy
the same ports, so nothing above `internal/adapters` changes with the choice.

| Backend            | Storage     | Queue           | Jobs        |
| ------------------ | ----------- | --------------- | ----------- |
| `memory` (default) | filesystem  | in-memory chan  | in-memory   |
| `aws`              | S3          | SQS             | DynamoDB    |

The AWS backend talks to LocalStack when `AWS_ENDPOINT_URL` is set, and to real
AWS when it is not; nothing else differs.

```sh
make aws-up                          # start LocalStack and create the resources
export IMAGEFORGE_BACKEND=aws
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=eu-west-1
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
make run-api                         # one terminal
make run-worker                      # another
```

| Variable                   | Default            | Purpose                         |
| -------------------------- | ------------------ | ------------------------------- |
| `IMAGEFORGE_BACKEND`       | `memory`           | `memory` or `aws`               |
| `AWS_ENDPOINT_URL`         | unset              | Endpoint override for LocalStack |
| `AWS_REGION`               | `eu-west-1`        | AWS region                      |
| `IMAGEFORGE_S3_BUCKET`     | `imageforge-media` | Bucket for originals and results |
| `IMAGEFORGE_SQS_QUEUE`     | `imageforge-jobs`  | Job queue name                  |
| `IMAGEFORGE_DYNAMODB_TABLE`| `imageforge-jobs`  | Job state table                 |

### Local AWS resources

`deployments/docker/localstack-init.sh` creates the bucket, the queue, its
dead-letter queue and the table. LocalStack runs it on startup — docker-compose
mounts it into `/etc/localstack/init/ready.d` — and `make aws-init` runs it by
hand. Every step is idempotent.

The queue carries a redrive policy, so a message that fails five times moves to
`imageforge-jobs-dlq` instead of cycling forever. Deliveries are settled
explicitly: the worker deletes a message once its job is done and returns it to
the queue otherwise, through the optional `ports.Acknowledger` interface that
`sqsqueue` implements and `memqueue` does not.

### Integration tests

`test/integration` runs the API and the worker against LocalStack and checks the
whole upload → process → result flow. It is behind the `integration` build tag,
and skips itself when no stack is reachable.

```sh
make aws-up
make test-integration
```
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

Work in progress. The domain model, ports, use cases, HTTP API and worker pool
are in place and tested, on two interchangeable backends: in-process adapters
that need nothing external, and S3, SQS and DynamoDB, verified end to end
against LocalStack. The Terraform deployment and the web front-end are not
implemented yet.
