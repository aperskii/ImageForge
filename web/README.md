# ImageForge web

A Vite + React + TypeScript + Tailwind front-end for the ImageForge API. Upload
an image, choose a transformation, and watch the job through to its result.

## Running it

The app is only useful with the API behind it, so start that first. From the
repository root:

```sh
make run-api      # one terminal — the Go API on :8080
make run-worker   # another — nothing drains the queue without it
```

Then, in `web/`:

```sh
npm install
npm run dev       # http://localhost:5173
```

Vite proxies `/uploads`, `/jobs`, `/auth`, `/healthz` and `/readyz` to
`http://localhost:8080`, so the browser only ever makes same-origin requests and
CORS never comes into it. Point it somewhere else with `IMAGEFORGE_API_URL`:

```sh
IMAGEFORGE_API_URL=http://localhost:9000 npm run dev
```

| Script            | What it does                              |
| ----------------- | ----------------------------------------- |
| `npm run dev`     | Dev server with the API proxy and HMR     |
| `npm run build`   | Type-check and build to `dist/`           |
| `npm run preview` | Serve the built output                    |
| `npm run lint`    | Type-check only                           |

## Seeing the result image

The comparison slider needs to fetch the transformed image, which means the API
has to tell it where that image lives. Start the API with a public base URL:

```sh
IMAGEFORGE_PUBLIC_BASE_URL=http://localhost:4566/imageforge-media make run-api
```

Without it the API returns a `result_key` but no `result_url`, and the app says
so instead of showing a broken image. Against LocalStack the URL above works
once `make aws-up` has created the bucket.

Note that **WebP and AVIF need the libvips backend**. A server built with
`-tags nogovips` will reject those formats, and the job comes back failed with
the reason shown in the UI.

## How it is put together

```
src/
  api/            Wire types and the fetch layer, mirroring internal/adapters/httpapi
  components/     Dropzone, transformation form, comparison slider, gallery
  hooks/          TanStack Query polling, and the browser-local job history
  App.tsx         Composes the upload flow and the result view
```

**Polling.** After a successful upload the app polls `GET /jobs/{id}` every
1.5s, and stops the moment the job reaches `done` or `failed`; a settled job
costs nothing to leave on screen. Polling continues while the tab is in the
background, because a job takes seconds and someone who switched away should
find the result waiting rather than a stale spinner. A job that never settles is
abandoned after five minutes rather than polled forever.

**Errors.** The API answers with RFC 7807 problem details. `type` is the stable
field the client switches on; the UI shows the `request_id`, which also appears
in the server log, so a screenshot from a user is enough to find the request.

**History.** The gallery lives in `localStorage`: the API has no per-user job
listing, so history is local to one browser and disappears with its site data.
Thumbnails are rendered from the local file on a canvas rather than fetched
back, which is what lets the gallery work even when results are not publicly
readable.

**Validation** is duplicated deliberately. The form catches the common mistakes
— no dimension set, a quality on lossless PNG, dimensions over the API's 10000px
limit — so they do not cost a round trip. The server validates again and remains
the authority.

## Authentication

The API requires a bearer token on `/uploads` and `/jobs/{id}`. The app fetches
one from `POST /auth/token` on its first request and keeps it **in memory**, not
in `localStorage`: a credential in storage is readable by any script that gets
injected into the page, and this one is cheap to replace.

If the server says a token is no longer good — it expired, or the server
restarted with a new ephemeral signing key — the client fetches a fresh one and
retries the request once. That silent retry is the difference between an app
that keeps working and one that fails until reloaded.

A `429` surfaces the `Retry-After` the API returned, so the message says when to
try again rather than just that something went wrong.
