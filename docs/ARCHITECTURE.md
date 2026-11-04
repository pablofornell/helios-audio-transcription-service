# HELIOS-ATS — Architecture

HELIOS-ATS (Audio Transcription Service) is one service inside Helios, this
organisation's domain for data extraction and manipulation — Helios names the domain,
ATS names this service within it, and the rest of this document says "ATS" for
everything specific to this codebase. This is the map for anyone about to change the
code; package doc comments (`go doc ./internal/...`) are the ground truth when the two
ever disagree.

## The two decisions that shape everything

### 1. Transcription is an async, Postgres-backed job queue, not a request/response call

A single upload can take longer than any HTTP client wants to hold a connection open
for: `large-v3` on an hour of audio is minutes of CPU-bound work, not milliseconds.
`POST /api/v1/transcriptions` therefore does the minimum work that has to happen
synchronously — validate `Options`, check free disk space, stream the upload to
storage, insert one row — and returns **202** with the row in `status: "pending"`. The
actual pipeline (probe, normalize, transcribe) runs later, off the request path,
picked up by an `internal/jobs.Pool` worker. There is no separate queue service: the
same `transcriptions` table `app.Repository` reads is the queue, claimed with
`SELECT ... FOR UPDATE SKIP LOCKED`:

```sql
WITH candidate AS (
    SELECT id FROM transcriptions
    WHERE status = 'pending' AND next_attempt_at <= now()
    ORDER BY next_attempt_at, created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE transcriptions SET status = 'running', ... FROM candidate ...
RETURNING ...
```

`SKIP LOCKED` means a concurrent `Claim` — another worker in this process, or a worker
in an entirely different process — simply never sees the row a winning `Claim` just
locked, so no two workers can ever be handed the same job. This survives process
restarts for free (a claimed-but-abandoned row is just a row with a lapsed lease) and
needs no broker to run, at the cost of polling (`HELIOS_WORKER_POLL_INTERVAL`, default
2s) instead of push-based dispatch — an acceptable trade at ATS's scale, and an
isolated one: swapping `internal/jobs` for a different dispatch mechanism later
touches nothing above `internal/app`.

### 2. `Repository` owns rows, `Queue` owns the state machine — no overlap

`internal/app/ports.go` splits persistence into two ports for one reason: a
caller-facing mutation (cancel, retry, delete) must never be able to clobber the lease
a worker currently holds, and a worker's lease-guarded write must never be reachable
from an ordinary CRUD call that doesn't even know what a lease is.

- **`Repository`** creates, reads, lists and deletes rows, plus the two caller-facing
  state changes that are not lease-guarded writes: `RequestCancel` and `Requeue`.
- **`Queue`** is every legal worker-side transition and nothing else: `Claim`,
  `Heartbeat`, `Finish`, `ReleaseExpired`, `PurgeTerminalBefore`. Every write here
  applies only if the row's current state and lease ownership permit it, and writes
  nothing at all otherwise (`ErrCodeConflict`).

One Go type (`postgres.Store`) implements both ports, because `Get`, `List`, `Claim`
and `PurgeTerminalBefore` all scan the same row shape and splitting that scanning code
across two types would just duplicate it — but the interfaces stay separate, and
`internal/app` is what enforces that nothing ever reaches for the wrong one.

## Why `ListFilter` and `Page` live in `domain`, not `app`

Go interface satisfaction requires exact type identity, not structural equivalence of
field names — a `postgres.ListFilter` that merely looks like `app.ListFilter` would
not satisfy `app.Repository`'s `List(ctx, ListFilter) (Page, error)` method set unless
it *is* that type. `internal/postgres` deliberately does not import `internal/app` (see
**Layout and the import direction**, below), so it cannot reference `app.ListFilter`
directly either. `internal/domain/listing.go` breaks the deadlock: `ListFilter` and
`Page` are declared once, in the leaf package both layers already depend on, and
`internal/app` and `internal/postgres` each declare a type alias
(`type ListFilter = domain.ListFilter`) rather than a copy. `app.ListFilter`,
`postgres.ListFilter` and `domain.ListFilter` are therefore the same type, not three
lookalikes, which is what lets `*postgres.Store` satisfy `app.Repository` and
`app.Queue` without ever importing `internal/app`.

## Layout and the import direction

```
internal/domain           leaf: Transcription, Status, Options, Audio, Result,
                           Segment, Word, ErrorCode, ListFilter, Page. No internal
                           imports.

internal/config            leaf: HELIOS_* env loading + validation.
internal/observability      leaf: slog, Prometheus, health-check aggregation, OTel.
internal/storage/localfs    leaf: path-hardened local filesystem store.

internal/audio                        imports domain. ffprobe/ffmpeg subprocess pipeline.
internal/transcriber, .../whispercpp  imports domain. whisper-cli subprocess pipeline.
internal/transcriber/fake             imports domain. deterministic test double.
internal/transcript                   imports domain. Result -> txt/srt/vtt renderers.
internal/postgres                     imports domain. pgx Repository + Queue + migrations.

internal/app                the business logic: Service + ports.go (Repository, Queue,
                             Store, Transcriber, AudioProcessor, HealthChecker, Clock,
                             IDGenerator). Imports domain only — never an adapter
                             package below.

        ┌──────────────────┬──────────────────┬──────────────────┐
        ▼                  ▼                  ▼                  ▼
internal/httpapi     internal/jobs      internal/retention   cmd/helios-transcription-api/wire
(REST delivery,       (worker pool,      (the retention        (composition root:
 Service caller)       lease/heartbeat/   sweep)                 constructs every
                        retry policy)                             adapter and wires
                                                                    them together)
```

The rule an implementer can check by hand: **adapters (`postgres`, `localfs`,
`whispercpp`, `audio`, `httpapi`, `jobs`, `retention`) import `internal/app` only to
implement or call its ports; `internal/app` never imports an adapter.**
`internal/domain` doesn't import `internal/app` at all — it's the leaf every port is
built from. `internal/config` and `internal/observability` don't import `internal/app`
either; they are read directly by `cmd/.../wire` and threaded in as plain values
(`*observability.Metrics`, `config.WorkerConfig`, ...).

## Request path and worker path

The two never overlap in time — `POST /transcriptions` never blocks on anything the
worker pool does, and the worker pool never touches an HTTP connection:

```
 REQUEST PATH (internal/httpapi -> internal/app, one HTTP round trip)

 client --POST /api/v1/transcriptions-->  httpapi.submitTranscription
                                                |
                                                v
                                          app.Service.Submit
                                            |          |
                                            v          v
                                     Store.Put   Repository.Create
                                  (source<ext>)  (row, status=pending)
                                            |          |
                                            +----+-----+
                                                 v
                                      202 + Transcription (status="pending")


 WORKER PATH (internal/jobs.Pool, off the request path, polling + lease-driven)

  claimLoop ---Queue.Claim (SKIP LOCKED)--> row (status=running, lease held)
     |                                            |
     |  every HeartbeatInterval:                  v
     |  Queue.Heartbeat ------------------>  AudioProcessor.Probe   (ffprobe)
     |  (extends lease,                            |
     |   reports cancel_requested)                 v
     |                                       AudioProcessor.Normalize (ffmpeg)
     |                                              |
     |                                              v
     |                                       Transcriber.Transcribe (whisper-cli)
     |                                              |
     v                                              v
  (lease lost or canceled -> abandon,         Queue.Finish
   never call Finish)                    (succeeded | failed | pending-retry)
```

## The per-job pipeline

Everything from here on runs inside a single `internal/jobs.Pool` worker goroutine,
never on the HTTP request path:

1. **Claim** a `pending` row (`FOR UPDATE SKIP LOCKED`, above) and start a heartbeat
   loop against it.
2. **Probe** the stored source with `ffprobe` (`internal/audio.Probe`): duration,
   sample rate, channel count, codec. A file with no audio stream at all is rejected
   here with `ErrCodeNoAudioStream`; one whose probed duration exceeds
   `HELIOS_AUDIO_MAX_DURATION` is rejected with `ErrCodePayloadTooLarge` — deliberately
   a job-time check, not an upload-time one: probing requires decoding the container,
   which `POST /transcriptions` does not do (see **Trade-offs**, below).
3. **Normalize** with `ffmpeg` (`internal/audio.Normalize`) to 16 kHz mono 16-bit PCM
   WAV at `transcriptions/<id>/audio.wav` — see **Why 16 kHz mono WAV**, below.
4. **Transcribe** the WAV with `whisper-cli` (`internal/transcriber/whispercpp`),
   parsing its `-oj`/`-ojf` JSON output into a `domain.Result`. This step runs two
   `whisper-cli` steps in order. First, `whisper-cli -dl` detects the audio language.
   The request never sets it: `Options.Language` is ignored. Detection runs on a clip
   (up to 30 s) cut with `ffmpeg` from the start of the audio with the leading silence
   removed. If the probability is under 0.5, it also tries clips at one third and two
   thirds of the audio and keeps the most probable answer. A low probability never
   fails a job. Then `domain.PlanOutput` applies the language rule: Spanish stays
   Spanish, English stays English, any other language becomes English by translation
   (`-tr`, with the translation model, because the turbo model cannot translate).
   `Options.Task` plays no part. `Config.Timeout` and the job timeout cover all
   processes, and all of them honour cancellation. A failed detection is a retryable
   `tool_failure`: the service never guesses a language. A missing translation model
   is a non-retryable `model_unavailable`, and an operator retries the job by hand
   after `make models`. `Transcriber.Available` does not check that model, so the
   service still starts and serves Spanish and English without it.
5. **Finish**: persist the `Result` (or `Error`) and the probed `Audio` metadata, then
   unconditionally delete the normalized WAV — success or failure, it is large,
   derived, and worth nothing once the job is done with it. The original upload is
   kept until retention purges it (see **Storage layout**, below).

## The lease/heartbeat/cancel protocol

Every claimed row carries `lease_owner` and `lease_expires_at`
(`HELIOS_WORKER_LEASE_DURATION`, default 1m). A worker that is still alive renews it
every `HELIOS_WORKER_HEARTBEAT_INTERVAL` (default 15s, and must be strictly less than
the lease duration — `config.Validate` enforces this, since a heartbeat interval equal
to or greater than the lease would let a lease expire *between* heartbeats even under
normal operation). A worker that dies — killed, crashed, a network partition to
Postgres — simply stops renewing, and `Queue.ReleaseExpired` (swept every
`HELIOS_WORKER_LEASE_DURATION`, by every worker, safe to run concurrently) returns the
row straight to `pending`, immediately claimable again. No row can be stuck `running`
forever because its worker vanished.

**`Heartbeat`'s return value is the only channel by which a cancel reaches a worker
that is already running a job.** This is easy to miss and load-bearing:
`POST .../cancel` (via `app.Service.Cancel` → `Repository.RequestCancel`) does nothing
to the worker directly — for a `running` row it only sets `cancel_requested = true` in
the database; the worker discovers this the next time it calls `Heartbeat` to renew its
own lease. `Heartbeat` extends the lease *only* while the row is still `running` under
that exact `lease_owner`, and separately reports `cancel_requested`. A worker that sees
anything other than "still running, lease renewed, not canceled" — a lost-lease
`ErrCodeConflict`, or `canceled: true` — must abandon the job immediately and must
never write a terminal state itself: `Queue.Finish` re-checks
`lease_owner = owner AND status = 'running'` as a second guard, so even a worker that
somehow ignored the heartbeat's answer cannot resurrect a row that has already been
reclaimed or canceled out from under it. `internal/jobs.heartbeatLoop` additionally
cancels the job's own `context.Context` the instant it notices either condition, which
is what actually stops a well-behaved `ffmpeg`/`whisper-cli` subprocess promptly —
`exec.CommandContext`'s `cmd.Cancel` kills the whole process group — rather than
waiting for the pipeline step in progress to finish on its own. That cancellation is an
optimisation layered on top, though: the heartbeat conflict itself is the mechanism
that must always work, including for a hypothetical future worker that never inspects
its own `ctx` between pipeline steps.

**What happens when a worker dies mid-job:** its lease simply stops being renewed.
Once `lease_expires_at` passes, `ReleaseExpired` returns the row to `pending`
unconditionally — `Attempts` is left exactly as the dead worker's `Claim` set it, and
`ReleaseExpired`'s own SQL does not check it against `MaxAttempts` before requeuing.
This is worth being precise about, because it differs from the automatic-retry path:
`internal/jobs.finish` (the code that runs when a worker *does* get to report an
outcome) only schedules a retry while `Attempts < MaxAttempts`, and marks the job
permanently `failed` otherwise — but `ReleaseExpired` never calls `finish` at all, so
that ceiling is not consulted on the lease-expiry path. Each `Claim` still increments
`Attempts` by one (`attempts = attempts + 1`, in the same `UPDATE` that sets
`status = 'running'`), so a job repeatedly claimed and then abandoned by a persistently
crashing or unreachable worker fleet will show a climbing `Attempts` count without ever
being auto-failed by that count alone. In practice this only matters if workers are
crashing *between* claiming a job and running it (or finishing it) repeatedly, which is
itself the condition worth alerting on — see `docs/RUNBOOK.md`'s guidance on a stuck
`pending` row with an unusually high `Attempts` relative to `MaxAttempts`.

## Retry policy

`domain.ErrorCode.Retryable()` is the single classification every failure funnels
through, and it is deliberately narrow: **`true` only for `ErrCodeToolFailure` and
`ErrCodeTimeout`**. `internal/jobs.finish` is the only place that consults it — a
failed attempt is retried (`status` back to `pending`, with a scheduled
`next_attempt_at`) only when both the error is retryable *and* the row's own
`Attempts < MaxAttempts`; otherwise it is marked permanently `failed`.
`Queue.Finish` itself just executes whichever transition `internal/jobs` decided; it
has no retry policy of its own.

The retry delay is exponential with full jitter (`internal/jobs/backoff.go`):
`min(BackoffBase * 2^(attempts-1), BackoffMax)`, then a delay drawn uniformly from
`[0, that value]`. Full jitter — rather than a narrower window centred on the capped
value — is the deliberate choice: under a correlated failure (every in-flight job's
`ffmpeg` or `whisper-cli` hitting the same transient condition within the same second,
e.g. a disk that just filled) it spreads the resulting retries far more evenly across
the retry window than a centred jitter would, trading a tighter minimum retry delay for
a much lower chance that every retry lands in the same narrow band and reproduces the
very thundering herd it exists to avoid.

**Why unclassified errors are deliberately not retryable.** `ErrCodeInternal` covers
everything that reached a `finish` call without being classified as one of the other
eleven codes at the point it was detected — and by design, that is treated as a *bug*,
not a transient condition, until proven otherwise. The alternative — retrying an
unclassified failure like any other — would silently burn `MaxAttempts` retries hiding
a bug that will fail identically every time (a nil pointer, a logic error, a genuinely
malformed WAV) behind a `pending` status an operator has no reason to look at twice.
Every failure mode ATS's own code can anticipate is already given its own code
(`ErrCodeUnsupportedMedia`, `ErrCodeNoAudioStream`, `ErrCodeModelUnavailable`, ...) at
the point it is detected — `internal/audio/classify.go` and
`internal/transcriber/whispercpp/classify.go` are both explicit tables mapping
stderr/exit-code shapes to a code, precisely so as little as possible falls through to
`ErrCodeInternal` by default. An operator can always retry an internal failure by hand
(`POST .../retry`) once the underlying bug is understood or fixed.

Two error codes are terminal for reasons worth spelling out individually:
`ErrCodeDiskFull` and `ErrCodeModelUnavailable` are not retryable *automatically*
because retrying immediately would fail identically — free space and model weights
don't materialise on their own — but both are exactly the kind of thing an operator
fixes and then retries by hand (see `docs/RUNBOOK.md`).

## Why whisper.cpp runs as a subprocess

`whisper-cli` is invoked with `exec.CommandContext`, once per job, in a per-job temp
directory — not linked in-process (there is no supported cgo/Go binding this service
depends on, and cgo would tie every build to whatever `libwhisper` happens to be
installed on the build host) and not run behind a Python sidecar (that would add a
second runtime, a second process-supervision problem, and a network or IPC hop for
zero benefit — whisper.cpp's own CLI already does everything ATS needs: JSON output,
word timestamps, translation, language auto-detection). A subprocess:

- is killed cleanly on cancellation or timeout (`cmd.Cancel` sends `SIGKILL` to the
  whole process group, not just the direct child — `whisper-cli` does not fork helpers
  the way `ffmpeg` can, but the pattern is shared with `internal/audio` for exactly
  that reason);
- crashing (a segfault in a malformed model file, an OOM kill) fails one job with
  `ErrCodeToolFailure`, not the whole `helios-transcription-api` process;
- lets model selection, thread count and every other whisper-cli flag
  (`internal/transcriber/whispercpp/argv.go`) be exactly whisper-cli's own documented
  surface, verified against its actual `--help` output and `examples/cli/cli.cpp`
  rather than guessed at.

The cost is process-start overhead per job (loading a `large-v3` model from disk is not
free) and no in-process way to share loaded model weights across jobs — an accepted
trade at ATS's scale (`HELIOS_WORKER_CONCURRENCY`, default 2) since correctness and
failure isolation matter more here than shaving milliseconds off startup.

## The audio normalization step, and why 16 kHz mono WAV

whisper.cpp's model architecture was trained on, and its C++ inference code expects,
16 kHz mono PCM audio — anything else either fails outright or silently produces worse
transcriptions (a stereo or higher-sample-rate file fed directly would be resampled
internally with no control over how). `internal/audio.Normalize` shells out to
`ffmpeg -vn -map 0:a:0 -ac 1 -ar 16000 -c:a pcm_s16le -f wav`, deliberately performing
this conversion once, explicitly, ahead of every `whisper-cli` invocation rather than
depending on whisper.cpp's own best-effort resampling — this is also the point where a
container with an attached video stream or multiple audio tracks (dub tracks,
commentary) is normalized down to exactly the one audio stream whisper.cpp will
transcribe (`-map 0:a:0`: the first audio stream, always). The normalized WAV is
written to a temp file and renamed into place atomically, so a killed or crashed
`ffmpeg` invocation never leaves a half-written WAV at the path `whisper-cli` would
otherwise be handed next.

## Storage layout and what retention reclaims

```
<HELIOS_STORAGE_ROOT_DIR>/transcriptions/<id>/source<ext>   the original upload, kept
                                                              for the transcription's
                                                              whole lifetime
<HELIOS_STORAGE_ROOT_DIR>/transcriptions/<id>/audio.wav      the normalized WAV,
                                                              present only while a
                                                              worker is actively
                                                              running that job
```

`source<ext>`'s extension is taken from the sanitized, lowercased upload filename (or
`.bin` if it has none); the file itself is written by `app.Service.Submit` before the
database row even exists, and removed (the whole `transcriptions/<id>` directory) if
anything after that point fails, so a half-written upload never lingers with no row to
account for it. `audio.wav` is written by the worker at the start of a job and removed
unconditionally when the job ends — success or failure — since it is entirely derived
from the source and only exists to be handed to `whisper-cli`.

Two things delete a transcription's files, both by removing the whole
`transcriptions/<id>` directory:

- **`DELETE /api/v1/transcriptions/{id}`** — immediate, caller-initiated, regardless of
  the job's current status.
- **`internal/retention.Sweeper`** — a background sweep that deletes every transcription
  that has been *terminal* (`succeeded`, `failed` or `canceled`) for longer than
  `HELIOS_RETENTION_MAX_AGE` (default 168h / 7 days), checking every
  `HELIOS_RETENTION_INTERVAL` (default 1h). `Queue.PurgeTerminalBefore` deletes the
  database rows first, atomically, in one `DELETE ... RETURNING`; the sweeper then
  best-effort removes each purged row's directory — a directory-removal failure is
  logged and skipped, never retried, since the database row (the only thing that made
  the files reachable at all) is already gone by the time that removal is attempted. A
  `pending` or `running` transcription, or one still holding an unexpired lease, is
  never touched, however old.

## The error taxonomy and its HTTP mapping

`domain.ErrorCode` (`internal/domain/errors.go`) is the one classification every
failure in the system funnels through, and it decides two independent things at once:

1. **The HTTP status** a failure maps to when surfaced synchronously as an
   `ErrorResponse` (the full table is in `docs/openapi.yaml`'s top-level description;
   e.g. `disk_full` → 507, `model_unavailable` → 503, `canceled` → 499). Most of these
   codes are far more commonly seen inside a *stored* Transcription's `error` field (a
   200 response body from `GET /transcriptions/{id}`) than as the status of the
   request that reads it — `unsupported_media` and `no_audio_stream` in particular can
   only ever appear that way, since ffprobe only runs once a job is claimed, well
   after `POST /transcriptions` has already returned 202.
2. **Whether a failed attempt is retried** — see **Retry policy**, above.

Every error crossing a package boundary in this service is a `*domain.Error`,
classified as close to the point of detection as possible:
`internal/audio/classify.go` and `internal/transcriber/whispercpp/classify.go` are both
tables of the exact stderr/exit-code shapes `ffmpeg`/`ffprobe`/`whisper-cli` produce,
so that a subprocess failure is classified once, correctly, rather than re-guessed at
every call site above it. `internal/httpapi/errors.go`'s `statusForCode` map is the
single place a `domain.ErrorCode` becomes an HTTP status; no handler picks a status
itself.

## Observability surface

- **Logging** — `log/slog`, JSON by default (`HELIOS_OBSERVABILITY_LOG_FORMAT`), a
  `request_id` folded into every request's context (`internal/httpapi`'s
  `requestLogger` middleware) so every log line from a single HTTP call, including a
  panic recovered by `recoverer`, can be correlated by hand.
- **Metrics** — a private Prometheus registry (never `prometheus.DefaultRegisterer`,
  so tests get fully isolated registries), every series prefixed `helios_`:
  `helios_transcriptions_submitted_total`, `helios_transcriptions_completed_total{status}`,
  `helios_transcription_duration_seconds{model}`, `helios_transcription_audio_seconds`,
  `helios_jobs_in_flight`, `helios_http_requests_total{method,route,status}`,
  `helios_http_request_duration_seconds{method,route}`, plus the standard Go
  runtime/process collectors. Served at `/metrics` when
  `HELIOS_OBSERVABILITY_METRICS_ENABLED` is true.
- **Health** — `/healthz` is unconditional liveness (200 the instant the process can
  answer HTTP at all); `/readyz` aggregates every registered `app.HealthChecker`
  (`internal/observability.Registry`), each bounded by its own timeout and run
  single-flight per name so one permanently wedged dependency check can never
  accumulate more than one leaked goroutine. `internal/jobs.Pool` itself is one such
  checker (`Name() == "worker_pool"`), reporting unhealthy when its claim loop has not
  run in over `3 * HELIOS_WORKER_POLL_INTERVAL` — the signature of a wedged worker
  pool that has not crashed the process.
- **Tracing** — OpenTelemetry via `otelhttp`, off by default
  (`HELIOS_OBSERVABILITY_TRACING_ENABLED=false`, no collector required to run ATS);
  when enabled, `HELIOS_OBSERVABILITY_OTLP_ENDPOINT` is required
  (`config.Validate` enforces this) and every request is wrapped in a span before any
  other middleware runs, so its duration covers the whole stack, not just the handler.

## Trade-offs and current limitations

Worth being explicit about, rather than discovered the hard way:

- **No authentication.** Every route in `docs/openapi.yaml` is open to anyone who can
  reach the service. ATS is designed to sit behind whatever perimeter (reverse proxy,
  network policy, an API gateway) an operator already runs in front of internal
  services — it has no notion of a caller identity of its own.
- **Single-node, local-filesystem storage.** `internal/storage/localfs` is rooted at
  one directory on one machine (`HELIOS_STORAGE_ROOT_DIR`, typically a Docker volume).
  There is no S3/object-storage backend, and `app.Store` has no replication or
  multi-node story — running more than one `helios-transcription-api` process against
  the same Postgres works for the *queue* (the lease mechanism is safe for any number
  of concurrent workers), but every one of those processes needs to see the exact same
  storage volume, or a worker that didn't write a given upload will fail to find it.
- **No speaker diarization.** whisper.cpp produces segments and (optionally) words,
  never speaker labels; this is out of scope by design, not a gap waiting to be
  filled by a config flag.
- **Model weights are provisioned out of band.** `HELIOS_TRANSCRIBER_MODEL_DIR` must
  already contain `ggml-<model>.bin` for every model `HELIOS_TRANSCRIBER_ALLOWED_MODELS`
  names, before the first job that needs one is claimed — ATS never downloads a model
  itself. `Transcriber.Available` (the `/readyz`-facing check) confirms the directory
  is readable, not that any particular model is present; a missing model is only
  discovered per-job, as `ErrCodeModelUnavailable`. See `docs/RUNBOOK.md` for how to
  provision them.
- **Duration limits are enforced late.** `HELIOS_AUDIO_MAX_DURATION` is checked at
  probe time (inside a worker), not at upload time — an oversized file is still
  accepted, stored and queued before being rejected on its first (and only) attempt.
  This is a consequence of probing requiring a real decode, which `POST
  /transcriptions` deliberately does not do on the request path.
