# HELIOS-ATS

**HELIOS-ATS** (Audio Transcription Service) accepts an audio file upload, stores it,
and asynchronously produces a precise transcript using **whisper.cpp** — segments,
word-level timestamps, language auto-detection and translation to English are all
supported. The transcript language follows one rule: Spanish audio gives a Spanish
transcript, and audio in any other language gives an English one (see
[Transcript language](#transcript-language)). Helios is this organisation's domain for data extraction and manipulation
in general; ATS is this one service inside that domain, not the domain itself — the
compound name keeps the two apart. It ships as a single Go binary: a REST API plus a
Postgres-backed job queue and worker pool, in one process.

## Prerequisites

To run the **service**:

- [Docker](https://docs.docker.com/get-docker/) + Docker Compose.

`ffmpeg`/`ffprobe` are baked into the service's container image
(`deploy/Dockerfile`), together with a Vulkan-enabled `whisper-cli` and Mesa RADV.
Model weights are large (hundreds of MB to a few GB each) and distributed
separately, so they are provisioned once rather than baked into the image.
See `docs/RUNBOOK.md`'s **Prerequisites** for exactly how.

To build from source: **Go 1.27** (`go.mod` pins `go 1.27`, built and tested against
`go1.27.1`).

## Quickstart

```
cp deploy/.env.example deploy/.env
docker compose -f deploy/docker-compose.yml up -d --build
```

(or `make compose-up` from the repo root — same thing; `make compose-logs` /
`make compose-down` alongside it.) This brings up Postgres and
`helios-transcription-api`, applying its database migrations automatically on
startup. Once it's up:

```
curl -s localhost:7462/healthz   # 200, empty body: process is alive
curl -s localhost:7462/readyz    # 200 + JSON: worker pool (and every other registered check) healthy
```

Submit a file and grab its id. `file` and `options` may be given in either order (curl above
sends `file` first, then `options`, since that reads naturally) — the API honours `options`
wherever it lands in the request:

```
curl -s -X POST localhost:7462/api/v1/transcriptions \
  -F file=@interview.mp3 \
  -F 'options={"wordTimestamps":true}'
```

```json
{
  "id": "7e3c2c1a-9e0b-4f2a-8a3a-1a2b3c4d5e6f",
  "status": "pending",
  "options": {
    "model": "large-v3-turbo-q5_0",
    "language": "",
    "task": "transcribe",
    "wordTimestamps": true,
    "initialPrompt": "",
    "beamSize": 5,
    "temperature": 0
  },
  "audio": {
    "filename": "interview.mp3",
    "mediaType": "audio/mpeg",
    "sizeBytes": 3481200,
    "durationSeconds": 0,
    "sampleRateHz": 0,
    "channels": 0,
    "codec": ""
  },
  "attempts": 0,
  "maxAttempts": 3,
  "createdAt": "2026-09-20T10:15:03.128Z",
  "updatedAt": "2026-09-20T10:15:03.128Z"
}
```

Poll until it resolves (`audio.*` fills in once a worker has probed the file; `result`
appears once it succeeds):

```
curl -s localhost:7462/api/v1/transcriptions/7e3c2c1a-9e0b-4f2a-8a3a-1a2b3c4d5e6f | jq '{status, audio, result}'
```

```json
{
  "status": "succeeded",
  "audio": {
    "filename": "interview.mp3",
    "mediaType": "audio/mpeg",
    "sizeBytes": 3481200,
    "durationSeconds": 214.32,
    "sampleRateHz": 44100,
    "channels": 2,
    "codec": "mp3"
  },
  "result": {
    "text": "Thanks for joining me today. Let's start with...",
    "language": "en",
    "outputLanguage": "en",
    "durationSeconds": 214.32,
    "model": "large-v3-turbo-q5_0",
    "task": "transcribe",
    "segments": [
      {"id": 0, "start": 0.0, "end": 3.2, "text": "Thanks for joining me today."}
    ]
  }
}
```

Or fetch the transcript directly, in whichever format you need:

```
curl -s 'localhost:7462/api/v1/transcriptions/7e3c2c1a-9e0b-4f2a-8a3a-1a2b3c4d5e6f/transcript?format=srt' -o interview.srt
```

## Configuration at a glance

Every setting is an environment variable prefixed `HELIOS_`, loaded and validated at
startup by `internal/config`. The full list, with every default, lives in
[`deploy/.env.example`](deploy/.env.example) — copy it to `deploy/.env` for Compose, or
export the variables directly for `go run`/`make run`. The ones worth knowing up front:

| Variable | Default | What it does |
|---|---|---|
| `HELIOS_HTTP_ADDR` | `:7462` | Listen address. |
| `HELIOS_DATABASE_DSN` | `postgres://helios:helios@localhost:5432/helios_ats?sslmode=disable` | Postgres connection string. Never logged in full. |
| `HELIOS_STORAGE_ROOT_DIR` | `/data` | Where uploaded sources and in-progress normalized WAVs are written. |
| `HELIOS_STORAGE_MIN_FREE_BYTES` | `1073741824` (1 GiB) | Uploads are refused once free space on this volume drops below it. |
| `HELIOS_TRANSCRIBER_KIND` | `whispercpp` | `whispercpp` for real transcription via the `whisper-cli` subprocess, `fake` for a deterministic, no-subprocess double (tests, local dev). |
| `HELIOS_TRANSCRIBER_MODEL_DIR` | `/models` | Holds `ggml-<model>.bin` weight files — see `docs/RUNBOOK.md`. |
| `HELIOS_TRANSCRIBER_DEFAULT_MODEL` | `large-v3-turbo-q5_0` | Used when a submission's `options.model` is empty. |
| `HELIOS_TRANSCRIBER_NO_GPU` | `false` | GPU takes priority by default. Set `true` to force CPU transcription (`--no-gpu`). |
| `HELIOS_TRANSCRIBER_GPU_DEVICE` | `0` | Nonnegative whisper-cli GPU index (`--device`), ignored in CPU mode. |
| `HELIOS_TRANSCRIBER_ALLOWED_MODELS` | `large-v3-turbo-q5_0` | Only `large-v3-turbo-q5_0` is supported; configuration rejects other names. |
| `HELIOS_TRANSCRIBER_TRANSLATION_MODEL` | `large-v3-q5_0` | Internal model that translates audio in a third language to English. Not a request option. The service does not need its file to start or to be ready. |
| `HELIOS_WORKER_CONCURRENCY` | `2` | How many transcriptions run at once per process. |
| `HELIOS_AUDIO_MAX_DURATION` | `4h` | Audio longer than this is rejected (at probe time — see `docs/ARCHITECTURE.md`). |
| `HELIOS_RETENTION_MAX_AGE` | `168h` (7 days) | How old a terminal (succeeded/failed/canceled) transcription must be before the retention sweep deletes it — database row and files both. See `docs/RUNBOOK.md`. |
| `HELIOS_CORS_ALLOWED_ORIGINS` | *(unset, CORS disabled)* | Comma-separated exact origins (`scheme://host[:port]`, no wildcard) allowed to make cross-origin browser requests against this API. Only needed when the web build of `desktop/frontend` (a browser tab, not the Wails app) is pointed at a server on a different origin than the one it was served from — see its own Settings → Server, and `desktop/frontend/src/lib/browser/serverConfig.ts`. The ordinary same-origin deployment never needs this. |

For a **Ryzen 7 7730U on Linux**, use the AMD Vulkan Compose override described in
the [runbook](docs/RUNBOOK.md#amd-vulkan-ryzen-7-7730u). It grants the service user
access to the AMD render node, selects Mesa RADV, and defaults to one job at a time.
The default model is `large-v3-turbo-q5_0`; use four CPU threads and measure with your own
audio before increasing concurrency. `scripts/check-vulkan.sh` checks device access
and, with a WAV file, verifies actual Vulkan transcription.

**Why `HELIOS_`, not `ATS_`.** These variables, the `helios_`-prefixed Prometheus
metrics on `/metrics`, the `github.com/pablofornell/helios-audio-transcription-service`
module path, and the `helios-transcription-api` binary all keep the domain's name
rather than the service's. That's deliberate, not a leftover from before the service
had its own name: the prefix names Helios, the domain this service lives in, shared
across every sibling service — not this one service alone. Don't rename them to
`ATS_`/`ats_`; it would just break every environment that reads them for no benefit.

## Things to know before you start

**Transcription is asynchronous — `POST /transcriptions` always returns 202, never a
finished result.** A single job can take minutes of CPU-bound work (`large-v3-turbo-q5_0` on an
hour of audio, in particular); the endpoint validates the request, stores the upload
and queues it, then a worker in the same process runs the actual pipeline. Poll
`GET /transcriptions/{id}` (or build a client that does) until `status` reaches
`succeeded`, `failed` or `canceled` — there is no webhook and no SSE stream.

**Model weights are provisioned separately.** Only `large-v3-turbo-q5_0` is
supported for new submissions. Run `make models` to fetch
`ggml-large-v3-turbo-q5_0.bin` and `ggml-large-v3-q5_0.bin`; see
`docs/RUNBOOK.md`'s **Prerequisites**. Missing default weights make the service
unready. Missing translation weights do not: only a job with audio in a third
language fails, with `model_unavailable`. Other model names are rejected as invalid
input. Existing jobs and transcripts retain their recorded model names.

<a id="transcript-language"></a>
**Transcript language.** The service detects the audio language before it
transcribes, then applies one rule:

| Audio language | Transcript | Model |
|---|---|---|
| Spanish (`es`) | Spanish. Never translated. | `large-v3-turbo-q5_0` |
| English (`en`) | English. | `large-v3-turbo-q5_0` |
| Any other language | English, translated. | `large-v3-q5_0` |

`options.language` and `options.task` are still accepted but ignored. The service
always detects the audio language and the detection alone sets the output: a request
with `language: "fr"` on Spanish audio gives a Spanish transcript, and `task=translate`
on Spanish audio also gives a Spanish transcript. The `result` carries `language` (the
language of the audio), `outputLanguage` (the language of `text`), `model` (the model
that produced the text) and `task` (what the engine did).

Detection works on a clip of up to 30 seconds (the most `whisper-cli` reads) from which
the leading silence is removed with `ffmpeg`. If the probability of the answer is low
(under 0.5), the service also tries clips at one third and two thirds of the audio and
keeps the most probable answer. It never fails a job for low confidence, because audio
without speech is valid. The log line `whispercpp: language detected` shows the chosen
language, its probability and the window. Audio that changes language later than these
windows keeps the language of the part the service detected. Detection loads the model
once or more before the transcription, so each job takes a few seconds longer.

**Speaker diarization is out of scope.** ATS produces segments and, optionally,
word-level timestamps — never speaker labels. There is also no authentication layer:
ATS is designed to sit behind whatever perimeter (reverse proxy, network policy) an
operator already runs in front of internal services, and storage is a single local
filesystem volume, not a distributed object store — see `docs/ARCHITECTURE.md`'s
**Trade-offs and current limitations** for the full list.

## More

- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — how the pieces fit together, for
  anyone about to change the code.
- [`docs/RUNBOOK.md`](docs/RUNBOOK.md) — for anyone operating it.
- [`docs/openapi.yaml`](docs/openapi.yaml) — the HTTP API contract.
- `make help` — every `make` target.

## Shared queue, catalogue, and download behavior

The queue shows running jobs first, then pending jobs in submission order.
It always loads every running and pending job, however many exist. It loads the
newest 100 jobs at first. A "Load older transcriptions" button loads 100 more
jobs each time. Each poll reads again every page you loaded, so each row shown is
as current as the server. The queue is the only list of failed and canceled jobs, so
"Clear completed" hides succeeded rows only. Failed and canceled rows stay until
you remove them.
Library uses `library=true` to list only finished transcriptions: succeeded jobs
with a result and stored source audio, ordered by completion time. Pending,
running, failed, and canceled jobs stay in the queue. Its cursor follows that
ordering, including submillisecond timestamps. Loaded pages and open details
continue updating while you browse or search.

Uploaded audio remains downloadable for pending, running, failed, and canceled
transcriptions. Use "View in Library" on a queue row to open its detail.
Transcript downloads require a successful job with a result.
Individual audio and transcript downloads stream through the browser's download
manager, with brief duplicate-click protection per URL; separate files can start
independently. The existing elapsed processing timer remains in use because this
service does not expose transcription progress percentages.

Run `npm test`, `npm run build`, and `npm run test:web` in `desktop/frontend`.
Playwright uses port 5188 by default; set `HELIOS_WEB_PORT` to choose another port.
Install its browser once with `npx playwright install chromium`. Deterministic web
fixtures cover ordering, source availability, and live detail updates. Set
`HELIOS_WEB_API_URL=http://localhost:7462` to include actual audio/transcript
streaming and pagination checks against an isolated validation backend containing
45 completed transcripts and a canceled upload with stored audio.
