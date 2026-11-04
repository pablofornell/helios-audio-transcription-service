# Shared workflow improvements from social media ingestion

## Scope

The existing storage meter and source-audio download are already shared.
Apply active-first queue/library ordering, global completion ordering with correct
pagination, live catalogue reconciliation, content eligibility, native streaming
download launch guards, and cleanup/fencing of asynchronous UI work.

Accepted uploads have stored audio before transcription finishes. Pending,
running, failed, and canceled jobs retain their source-audio download; transcript
export requires a successful job with an actual result.

> Update 2026-11-04: This decision is replaced for the Library list. The Library
> now lists only finished jobs: status succeeded, with a result and stored audio.
> Pending, running, failed, and canceled jobs appear only in the Transcribe queue.
> Their source-audio download stays available from the queue row's "View in
> Library" detail view. The single-item and audio endpoints do not change. No ZIP/bulk workflow or
video extraction is introduced. Keep the existing elapsed-time display: this
worker does not expose the progress percentage needed by the ingestion ETA.

## Execution

1. Add optional Library ordering/content filter across REST, clients and storage,
   matching cursor ordering; reconcile paged rows and detail updates while polling.
2. Order accepted queue work before upload placeholders, clean animation timers,
   and fence asynchronous callbacks on unmount and server changes.
3. Share native attachment streaming and per-URL launch protection across audio
   and transcript controls; guard export eligibility and dialog lifecycles.
4. Review requirements and correctness independently; run regression tests, builds,
   backend suites, and actual browser tests against fixtures and a local REST API.
5. Commit, integrate into main, verify the merged result, and push at completion.

## Observed completion

All scoped changes are complete. Independent specification and correctness
reviews passed after regressions for filtered-out live details, canceled/reopened
delete dialogs, delayed native file selection, and synchronous server changes.

- Frontend: 226 tests passed in 25 files; TypeScript and production build passed.
- Backend: full Go suite passed, including PostgreSQL and end-to-end tests;
  PostgreSQL/HTTP/client race suites and targeted Go vet passed.
- Chromium: 11 scenarios passed with deterministic fixtures and an isolated REST
  server, covering active/FIFO ordering, source availability in every lifecycle
  state, live detail even when search hides its row, global completion pagination,
  real independent audio/transcript downloads, and canceled source-audio download.
- Formatting and whitespace checks passed. No desktop GUI was launched.

The storage meter and existing audio download remain supported. No absent ZIP or
bulk workflow, video extraction, or speculative progress percentage was added.
