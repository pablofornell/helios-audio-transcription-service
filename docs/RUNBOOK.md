# HELIOS-ATS — Runbook

For whoever is operating HELIOS-ATS (Audio Transcription Service) — the service this
organisation runs inside Helios, its data-extraction-and-manipulation domain — written
for the 3am case: specific commands, what the output looks like when things are fine,
and what it means when it doesn't.

## Prerequisites

To run the **service** via Compose:

- [Docker](https://docs.docker.com/get-docker/) + Docker Compose.

`ffmpeg` and `ffprobe` are baked into the service's container image
(`deploy/Dockerfile`), so you don't need them on the host to run `docker compose up`.
The image also includes a Vulkan-enabled `whisper-cli` and Mesa RADV. Model weights
are provisioned separately — see **`whisper-cli` and model weights**, below.

To build from source, or to run `helios-transcription-api` outside its container:

- **Go 1.27** (`go.mod` pins `go 1.27`, built and tested against `go1.27.1`).
- `ffmpeg`/`ffprobe` and `whisper-cli` **on PATH** (or point
  `HELIOS_AUDIO_FFMPEG_BINARY` / `HELIOS_AUDIO_FFPROBE_BINARY` /
  `HELIOS_TRANSCRIBER_BINARY` at their absolute paths).
- A Postgres instance reachable at `HELIOS_DATABASE_DSN`.

### `whisper-cli` and model weights

ATS shells out to whisper.cpp's own CLI, `whisper-cli`, and supports only the
quantized `large-v3-turbo-q5_0` model for new submissions. Both
`HELIOS_TRANSCRIBER_DEFAULT_MODEL` and `HELIOS_TRANSCRIBER_ALLOWED_MODELS` must name
`large-v3-turbo-q5_0`; configuration rejects other models. The weights must be
`ggml-large-v3-turbo-q5_0.bin` under `HELIOS_TRANSCRIBER_MODEL_DIR` (default
`/models`). ATS does not download weights automatically.

ATS also uses a second model, `large-v3-q5_0` (`HELIOS_TRANSCRIBER_TRANSLATION_MODEL`),
for audio in a third language (not Spanish, not English). The turbo model is not
trained for translation: with `-tr` it returns the original language. The translation
model is an internal choice, not a request option, and it is not in
`HELIOS_TRANSCRIBER_ALLOWED_MODELS`. Its weights are
`ggml-large-v3-q5_0.bin` in the same directory.

1. Outside Docker, build or install `whisper-cli` from
   [ggml-org/whisper.cpp](https://github.com/ggml-org/whisper.cpp) and put it on
   `PATH`, or point `HELIOS_TRANSCRIBER_BINARY` at it directly.
2. Run `make models` to fetch both weights into this repository's `./models`
   (`make models MODEL=large-v3-q5_0` fetches one). For a native deployment with
   another model directory, use:
   ```
   ./models/download-ggml-model.sh large-v3-turbo-q5_0 /models
   ./models/download-ggml-model.sh large-v3-q5_0 /models
   ```
   from the whisper.cpp repository, or fetch `ggml-large-v3-turbo-q5_0.bin` and
   `ggml-large-v3-q5_0.bin` from
   [huggingface.co/ggerganov/whisper.cpp](https://huggingface.co/ggerganov/whisper.cpp)
   into `HELIOS_TRANSCRIBER_MODEL_DIR`.

A missing default model makes the service unready: `Transcriber.Available`
confirms that the model directory is readable and that the configured model file
is present. It does **not** check the translation model. A deploy without
`ggml-large-v3-q5_0.bin` starts, reports ready, and transcribes Spanish and English
audio. A job with audio in a third language fails with `model_unavailable` until the
file is present (see **When a model is missing**, below). The bind mount is
`../models:/models:ro`, so the file must exist on the host: run `make models` there
after the deploy. Submissions naming another model are rejected as invalid input.
Existing jobs and transcripts keep their historical model names; no database
migration or deletion is required. Old browser composer selections reset to the
supported server default when loaded.

### AMD Vulkan (Ryzen 7 7730U)

The container builds the pinned whisper.cpp release with `GGML_VULKAN=ON`, using
Ubuntu 24.04 for its Vulkan build/runtime dependencies, as in the
[upstream Vulkan image](https://github.com/ggml-org/whisper.cpp/blob/v1.9.4/.devops/main-vulkan.Dockerfile).
Vulkan works through the host's Linux `amdgpu` kernel driver and the container's
[Mesa RADV driver](https://docs.mesa3d.org/drivers/radv.html). It does not require
ROCm or `/dev/kfd`. The default model is `large-v3-turbo-q5_0`. The translation model
`large-v3-q5_0` is about twice as large and slower. A job runs its processes one after
the other (detection, then transcription), so one job never holds two models at once.
The GPU shares RAM with the CPU;
use four CPU threads and one worker on the 7730U. These are starting settings, not a throughput
guarantee. Compare representative audio in GPU and CPU mode.

GPU has priority over CPU by default (`HELIOS_TRANSCRIBER_NO_GPU=false`):
whisper.cpp selects the configured Vulkan device first and schedules supported
operations there. CPU still handles operations that the GPU backend cannot execute
and serves as a fallback if no usable GPU is found. Set
`HELIOS_TRANSCRIBER_NO_GPU=true` only to deliberately force CPU transcription.

Find the AMD render node on the backend host: each
`/sys/class/drm/renderD*/device/driver` symlink identifies its kernel driver.
Use the node whose driver is `amdgpu` (it may be `renderD129` on a system with
multiple GPUs). Its group ID comes from `stat -c '%g' /dev/dri/renderD128`, with
the path changed to match your node. In `deploy/.env`, set:

```dotenv
HELIOS_RENDER_DEVICE=/dev/dri/renderD128
HELIOS_RENDER_GID=992
HELIOS_TRANSCRIBER_NO_GPU=false
HELIOS_TRANSCRIBER_GPU_DEVICE=0
HELIOS_TRANSCRIBER_DEFAULT_MODEL=large-v3-turbo-q5_0
HELIOS_TRANSCRIBER_THREADS=4
HELIOS_WORKER_CONCURRENCY=1
```

`992` above is an example: use the actual host group ID. The override preserves the
chosen node's path inside the container so RADV's sysfs discovery finds it, and
adds its numeric group to the existing nonroot service user. No privileged mode or
root user is needed. `VK_DRIVER_FILES` selects RADV only, preventing lavapipe software Vulkan
from being mistaken for GPU acceleration. `HELIOS_TRANSCRIBER_GPU_DEVICE` is the
whisper-cli GPU index, independent of the render node number.

Fetch the weights with `make models`, then use `make compose-up-vulkan`
(equivalent to Compose with both `deploy/docker-compose.yml` and
`deploy/docker-compose.vulkan.yml`). Keep both files in subsequent Compose
operations that recreate containers. The ordinary base Compose remains usable on
hosts without a GPU; `HELIOS_TRANSCRIBER_NO_GPU=true` forces the CPU path.

Run `./scripts/check-vulkan.sh` after building to check RADV access as the service
user. Pass a host WAV path, e.g. `./scripts/check-vulkan.sh /path/to/sample.wav`,
to run a real transcription and require `using Vulkan0 backend` (or your selected
index) in whisper-cli's diagnostics. Use a short mono 16 kHz PCM WAV readable by
the container user. The check runs in a disposable container without touching
the database or stored jobs. Inference output stays in that container's `/tmp`.

The service's `/readyz` checks the binary and model files; it does not initialize
the GPU. whisper.cpp may fall back to CPU when a GPU cannot initialize, so a
healthy API alone does not prove Vulkan use. The audio smoke check verifies backend
selection and catches this fallback. For GPU startup failures, check the render
node, its group ID and the `vulkaninfo` output first. GPU indices that do not exist
on the deployment host can only be detected at runtime.

For a native deployment, build whisper.cpp v1.9.4 with the
[documented Vulkan option](https://github.com/ggml-org/whisper.cpp/blob/v1.9.4/README.md#vulkan-gpu-support)
and install its Vulkan SDK build dependencies (`libvulkan-dev`, `glslc`,
`spirv-headers` on Ubuntu 24.04). The host also needs `libvulkan1`,
`mesa-vulkan-drivers`, and render-node access for the service account. Set
`HELIOS_TRANSCRIBER_BINARY` to that `whisper-cli`; the Go API needs no CGO changes.

## Running the stack

```
docker compose -f deploy/docker-compose.yml up -d --build
```

(`make compose-up` is the same thing; `make compose-logs` tails every service; `make
compose-down` stops and removes the containers, leaving the named volumes in place.)

Two services:

- **`postgres`** — the job queue and the catalogue. Has a `pg_isready`-based
  healthcheck; the API service waits for it (`depends_on: condition: service_healthy`)
  before starting.
- **`helios-transcription-api`** — the HTTP API (port **7462**) *and* the worker pool,
  in one process. There's no separate worker deployment to manage —
  `HELIOS_WORKER_CONCURRENCY` (default `2`) controls how many transcriptions this one
  process runs at once. Its `/data` volume is where every uploaded source and
  in-progress normalized WAV lands (see `docs/ARCHITECTURE.md`'s storage layout).

Both are named volumes managed by Compose; confirm the exact names for your deployment
(`docker volume ls`) before scripting a backup around them — see **Backup and
restore**, below.

Copy `deploy/.env.example` to `deploy/.env` and edit it to change any default before
bringing the stack up — `docker-compose.yml` reads it automatically.

Outside Compose: `make build` produces the `helios-transcription-api` binary; `make
run` runs it directly (`go run ./cmd/helios-transcription-api`), reading every
`HELIOS_*` variable from the process environment.

## Migrations

Migrations are plain SQL files (`internal/postgres/migrations/*.sql`, goose format),
embedded into the `helios-transcription-api` binary — there is no separate migration
image or step to keep in sync with a release.

By default (`HELIOS_DATABASE_AUTO_MIGRATE=true`) `helios-transcription-api` applies
every pending migration itself at startup, before it starts serving traffic or
claiming jobs — this is what makes `docker compose up` work on a completely fresh
database with no extra step.

To apply migrations **without** starting the server:

```
HELIOS_DATABASE_DSN='postgres://helios:helios@localhost:5432/helios_ats?sslmode=disable' \
  make migrate
```

Safe to run repeatedly — goose tracks applied versions in its own `goose_db_version`
table, and a run with nothing pending is a no-op. If you disable
`HELIOS_DATABASE_AUTO_MIGRATE`, this is how the schema gets applied instead — do it
before the first `helios-transcription-api` you point at that database starts, or it
fails at startup querying a table that doesn't exist yet.

To check what's applied directly:

```
psql "$HELIOS_DATABASE_DSN" -c 'select version_id, is_applied from goose_db_version;'
```

## Health and readiness

```
curl -s localhost:7462/healthz   # liveness
curl -s localhost:7462/readyz    # readiness
```

`/healthz` is unconditional — 200 the instant the HTTP handler runs at all, empty
body. It never touches the database, storage or the worker pool; if the process can't
reach those, that's what `/readyz` is for. A `/healthz` that doesn't respond at all
means the process is down or wedged before it can even accept a connection — check
`docker compose logs helios-transcription-api` / whether the container is even
running, not this endpoint.

`/readyz` runs every registered `app.HealthChecker` concurrently and returns the
aggregate — 200 if every check passed, 503 otherwise — the body is *always* the same
report either way, so you see exactly which check failed without a second request:

```json
{
  "ok": true,
  "checks": {
    "worker_pool": {"ok": true}
  }
}
```

`worker_pool` (`internal/jobs.Pool`) reports unhealthy when its claim loop has not run
in over `3 * HELIOS_WORKER_POLL_INTERVAL` — the signature of a wedged worker pool that
has not crashed the process (a claim-loop goroutine that panicked without bringing down
the process, or a database call that never returns and never times out). The Docker-level `HEALTHCHECK` calls
`/readyz`, so a container that never reaches `healthy` is either not coming up at all
or has a dependency down -- an unreachable database, an unwritable `/data`, or a
missing model file under `/models`. Check
`docker compose logs helios-transcription-api` for why — most commonly a bad DSN,
Postgres not actually reachable yet, or a `config.Validate` rejection logged at the
very first line.

## `/metrics`

Prometheus text format, `helios_`-prefixed, plus the standard Go runtime/process
collectors, served when `HELIOS_OBSERVABILITY_METRICS_ENABLED=true` (the default). The
series worth alerting on:

| Metric | Type | Alert on |
|---|---|---|
| `helios_transcriptions_completed_total{status="failed"}` | counter | A sustained rate increase — one model or one error class breaking, not the occasional permanent failure. |
| `helios_jobs_in_flight` | gauge | Pinned at `HELIOS_WORKER_CONCURRENCY` for a long stretch — the pool is saturated; either raise concurrency or investigate why jobs are running long (compare against `helios_transcription_duration_seconds`). |
| `helios_transcription_duration_seconds{model="..."}` | histogram | A sudden jump for one model — a slow host, thread starvation (`HELIOS_TRANSCRIBER_THREADS`), or a model swap. |
| `helios_http_requests_total{status=~"5.."}` | counter | Any sustained rate — these are bugs or dependency outages, not client mistakes (those are 4xx). |

`helios_http_request_duration_seconds` and `helios_transcription_duration_seconds` are
histograms (`prometheus.DefBuckets`) — useful for a p99 latency panel, not for
alerting directly. `helios_http_requests_total`'s `route` label is the router's
matched pattern (e.g. `/api/v1/transcriptions/{id}`), never the raw path, so a client
hammering different transcription IDs never turns this into an unbounded-cardinality
series.

## Reading the job queue in SQL

Every transcription is one row in `transcriptions`. A few queries worth having ready:

```sql
-- What's pending right now, oldest first (what claimAndStart will pick up next).
SELECT id, attempts, max_attempts, next_attempt_at, created_at
FROM transcriptions
WHERE status = 'pending'
ORDER BY next_attempt_at, created_at
LIMIT 20;

-- What's running, and whether its lease is healthy.
SELECT id, lease_owner, lease_expires_at, started_at,
       lease_expires_at < now() AS lease_expired
FROM transcriptions
WHERE status = 'running'
ORDER BY started_at;

-- Recently failed jobs, with the classified error code.
SELECT id, attempts, max_attempts, error->>'code' AS error_code, error->>'message' AS error_message, updated_at
FROM transcriptions
WHERE status = 'failed'
ORDER BY updated_at DESC
LIMIT 20;

-- How big is the backlog, by status.
SELECT status, count(*) FROM transcriptions GROUP BY status;
```

`options`, `audio`, `result` and `error` are all `JSONB` — `->>'field'` (text) or
`->'field'` (jsonb) reach into them directly, as above.

## When jobs pile up in `pending`

`helios_jobs_in_flight` pinned at `HELIOS_WORKER_CONCURRENCY`, or a growing count from
`SELECT count(*) FROM transcriptions WHERE status = 'pending'`, means workers are
saturated or stalled. In order:

1. **Confirm the claim loop is actually running** — `/readyz`'s `worker_pool` check
   (above). If it's unhealthy, the process needs a restart; check its logs for a panic
   or a wedged database call first.
2. **Check for jobs stuck `running` with an expired lease** — the `lease_expired`
   query above. A handful, transiently, is normal (a worker between heartbeats); a
   growing number that never clears means `ReleaseExpired` isn't running, or every
   worker in the fleet is down. `ReleaseExpired` runs every `HELIOS_WORKER_LEASE_DURATION`
   on every live worker — a row like this recovers within one lease window on its own
   once at least one worker is healthy again. **A `pending` row whose `attempts` is
   well above `max_attempts`** is the signature of a different problem — a worker
   fleet that keeps claiming this specific job and then dying before it can even
   report an outcome (crash-looping, an OOM kill, a poisoned input that crashes the
   process rather than failing cleanly). `ReleaseExpired` requeues an expired lease
   unconditionally, without checking `attempts` against `max_attempts` (unlike an
   ordinary automatic retry, which does — see `docs/ARCHITECTURE.md`), so a job like
   this will keep cycling through workers rather than ever being auto-failed. Treat it
   as a worker-crash investigation, not a transcription problem — `cancel` it by hand
   if it's not worth the debugging.
3. **Raise `HELIOS_WORKER_CONCURRENCY`** if the pool is healthy but genuinely
   under-provisioned for the incoming rate — check `helios_transcription_duration_seconds`
   first to confirm jobs are actually finishing in a reasonable time, not just piling
   up because each one is very slow (a large model on a small host looks identical to
   a stalled pool from the metric alone).
4. **Check `helios_transcriptions_completed_total{status="failed"}`** — a burst of
   `model_unavailable` or `disk_full` failures (see **Troubleshooting by error code**,
   below) means every claimed job is failing fast and re-queuing (if retryable) or
   piling up as `failed` (if not) rather than actually draining the backlog.

## When a model is missing

Historical jobs keep their original model name. Removing obsolete weights does not
delete their stored audio or transcripts. Resubmit historical failed audio using
the supported model rather than fetching obsolete weights.

A job fails with `error.code: "model_unavailable"` (not retried automatically — see
`docs/ARCHITECTURE.md`'s retry policy) when `HELIOS_TRANSCRIBER_MODEL_DIR` has no
`ggml-large-v3-turbo-q5_0.bin` for a supported transcription, or no
`ggml-large-v3-q5_0.bin` for audio in a third language. The error message names the
missing model. Restore the file, then retry the job(s):

```
ls "$HELIOS_TRANSCRIBER_MODEL_DIR"        # confirm what's actually there
make models                                # on the host: fetches both models, skips present files
curl -X POST localhost:7462/api/v1/transcriptions/<id>/retry
```

The service never falls back to the turbo model for a translation: turbo would return
the original language, so the job fails instead. The desktop app shows a Retry control
for this code. The retry endpoint re-queues any `failed` job, whatever its error code.

There's no bulk-retry endpoint; script over
`GET /api/v1/transcriptions?status=failed` if you need one. If several jobs failed for
the missing model, restoring the weights and retrying each is the correct fix, not
raising `HELIOS_WORKER_MAX_ATTEMPTS` — a missing file will not appear on its own no
matter how many attempts are configured, which is exactly why this code is not in
`ErrorCode.Retryable()`'s set.

## When the disk fills

`HELIOS_STORAGE_MIN_FREE_BYTES` (default 1 GiB) is `app.Service.Submit`'s own
free-space floor, checked live against `Store.FreeBytes()` before anything is written
for a new upload — not cached, so it recovers on its own the moment space is freed;
there is nothing to restart.

1. **New submissions are rejected outright**, `507` with `error.code: "disk_full"`,
   rather than being queued to fail later mid-normalize. A job already claimed when
   the disk fills can still hit the same condition writing its normalized WAV or its
   `whisper-cli` output — that surfaces as `disk_full` too (via
   `internal/audio/classify.go` / `internal/transcriber/whispercpp/classify.go`
   recognising `ENOSPC`/"no space left on device"), and is not retried automatically
   for the same reason a missing model isn't (see above).
2. **Free space, then retry the affected jobs by hand** — there is no persisted
   "disk full" state to clear; the next `Submit` or the next `retry` simply re-checks
   free space live.
3. To free space without touching unrelated data: find the largest completed
   transcriptions and delete them —
   `GET /api/v1/transcriptions?status=succeeded&limit=...`, sorted by
   `audio.sizeBytes` client-side (the API doesn't sort by size), then
   `DELETE /api/v1/transcriptions/{id}` per candidate — removes the row and its
   stored files in one call. Don't delete files directly under
   `HELIOS_STORAGE_ROOT_DIR`; that leaves an orphaned database row pointing at nothing
   (see **Backup and restore**, below, for why that pairing matters).
4. Or simply wait for the next `internal/retention.Sweeper` pass — old terminal jobs
   past `HELIOS_RETENTION_MAX_AGE` are purged automatically (see
   `docs/ARCHITECTURE.md`); lowering `HELIOS_RETENTION_MAX_AGE` (then restarting)
   reclaims space faster if the backlog is mostly old, finished work.
5. If the volume itself is undersized rather than just full of legitimate
   transcriptions, resize/replace it and raise `HELIOS_STORAGE_MIN_FREE_BYTES` back to
   a sane floor for the new capacity — the default is a "don't run the volume
   completely dry" floor, not a sizing recommendation for real audio traffic.

## Cancel or retry a stuck job

```
curl -X POST localhost:7462/api/v1/transcriptions/<id>/cancel   # pending or running -> canceled
curl -X POST localhost:7462/api/v1/transcriptions/<id>/retry    # failed or canceled -> pending, attempts reset
```

`cancel` on a `pending` job takes effect immediately (there is no worker to notify).
`cancel` on a `running` job sets a flag the worker discovers at its next
`HELIOS_WORKER_HEARTBEAT_INTERVAL` tick — expect up to that long before `status`
actually reads `"canceled"`; if it's been substantially longer, the worker holding
that lease may itself be wedged (see **When jobs pile up in pending**'s lease-expiry
check, above — the same lease mechanism recovers a canceled-but-unresponsive job just
like a dead one). Both endpoints are 409 (`error.code: "conflict"`) from any status
that doesn't permit the transition — a `cancel` on an already-terminal job, or a
`retry` on anything but `failed`/`canceled`.

## Backup and restore

**Both the database and the storage volume are needed together — one without the other
is useless.** A `transcriptions` row's stored source only means something if the file
is still at that path under `HELIOS_STORAGE_ROOT_DIR`; a file with no matching row is
an orphan ATS itself can never see or clean up (nothing lists the filesystem
directly). Restore one without the other and you get either rows that 404 on `.../audio`,
or files nothing references.

Database:

```
docker compose -f deploy/docker-compose.yml exec postgres \
  pg_dump -U helios -d helios_ats -Fc -f /tmp/helios-ats.dump
docker cp deploy-postgres-1:/tmp/helios-ats.dump ./helios-ats-$(date +%F).dump
```

Storage volume (adjust the volume and container names to what `docker volume ls` /
`docker ps` actually show you for your deployment):

```
docker run --rm \
  -v deploy_helios-data:/data:ro \
  -v "$PWD":/backup \
  alpine tar czf /backup/helios-ats-data-$(date +%F).tar.gz -C /data .
```

Take both at (as close to) the same instant as you can manage — there is no
transactional guarantee tying a database snapshot to a filesystem snapshot across two
separate `docker` commands, so a job that completes between the two backups can
produce a database row with no corresponding file in that backup set (harmless: a
retry re-normalizes and re-transcribes from nothing, since only the source upload is
kept anyway) or, more awkwardly, a file with no row (until pruned by hand). Restore in
the same pairing: `pg_restore` the dump into a fresh `postgres` volume, untar the
storage archive into a fresh data volume, bring the stack up against both together.

## Troubleshooting by error code

Every `error.code` you'll see, either in an API response or inside a transcription's
`error` field, and what it means:

| `error.code` | HTTP status | Retried automatically? | Meaning / what to do |
|---|---|---|---|
| `invalid_input` | 400 | no | Malformed request — bad `Options` field, unparsable `limit`/`status`/`cursor`, non-UUID `id`. Client-side; fix the request. |
| `unsupported_media` | 415 | no | `ffprobe` could not demux the container/codec at all. Never seen synchronously — only inside a stored job's `error`. The file will never decode; re-encode it client-side and resubmit rather than retrying. |
| `no_audio_stream` | 422 | no | The file decodes but carries no audio track (e.g. a silent video export). Same as above — resubmit a file with audio, don't retry the same one. |
| `payload_too_large` | 413 | no | Upload exceeded `HELIOS_HTTP_MAX_UPLOAD_BYTES`, or probed audio duration exceeded `HELIOS_AUDIO_MAX_DURATION`. Split or trim the source and resubmit. |
| `not_found` | 404 | — | No transcription with that id. |
| `conflict` | 409 | — | The requested transition isn't legal from the job's current status (e.g. `cancel` on a terminal job, `retry` on a non-terminal one, `.../transcript` before `succeeded`). |
| `disk_full` | 507 | no | See **When the disk fills**, above. |
| `model_unavailable` | 503 | no | See **When a model is missing**, above. |
| `tool_failure` | 502 | **yes** | `ffmpeg`/`ffprobe`/`whisper-cli` exited non-zero or produced unparsable output for a reason not otherwise classified — tool flakiness is common enough to default to retryable. Check `error.details.stderr` (or the `Details["stderr"]` on a failed job) for the actual tool output. A *sustained* rate of these (see `/metrics`, above) is worth investigating as a real bug, not flakiness. |
| `timeout` | 504 | **yes** | An operation (or the whole job) exceeded its deadline, including a lease that lapsed because the worker died mid-job. Check whether `HELIOS_AUDIO_TIMEOUT`/`HELIOS_TRANSCRIBER_TIMEOUT`/`HELIOS_WORKER_JOB_TIMEOUT` are simply too tight for the audio lengths and models actually in use. |
| `canceled` | 499 | no | Intentional — the cancel endpoint, or a heartbeat that noticed `cancel_requested`. |
| `internal` | 500 | **no, by design** | Unclassified — treated as a bug until proven otherwise, specifically so it surfaces instead of quietly burning `HELIOS_WORKER_MAX_ATTEMPTS` retries. Check the server logs for the underlying error (`internal/observability`'s structured logs carry a `request_id` you can correlate for an HTTP-triggered one; a worker-detected one logs `transcription_id` instead). See `docs/ARCHITECTURE.md`'s retry policy for why this one is deliberately not retried. |

A `failed` transcription is retried by hand with `POST .../retry` (see **Cancel or
retry a stuck job**, above); a `pending` one with a future `next_attempt_at` and a
non-empty `error` is not yet `failed` — it is waiting on its own scheduled automatic
retry (`error.code` will be `tool_failure` or `timeout`, the only two that ever
reach a `pending` row this way).
