# Export contract — `src/components` and `src/lib`

This is what the Transcribe and Library feature screens should code
against. Everything listed here is stable; if you need something not
listed, add it to the primitives layer rather than reimplementing it
inside a feature screen — StatusPill and Row in particular are shared
state (see their own docs below on why).

Import from the barrels: `import { Button, StatusPill } from "../../components"`
and `import { useTranscriptions, submit } from "../../lib"`. Do not import
`wailsjs/**` directly — `lib/api.ts` is the only file that does (this
service has no SSE endpoint, so there is no `lib/events.ts` here — the
live view is `lib/store.ts` polling `list()` instead, see below).

All CSS is scoped via CSS Modules; global tokens (`--bg-app`,
`--st-running`, `--space-3`, ...) are available everywhere without an
import once `src/styles/index.css` is loaded (it is, once, from
`main.tsx`) — see `src/styles/tokens.css` for the full token list and
`DESIGN.md` §2.2 for what each one means.

## `src/components`

| Component | Props (name: type — description) | Notes |
|---|---|---|
| **Button** | `variant?: "primary"\|"neutral"\|"ghost"\|"danger"` (default `neutral`) · `size?: "sm"\|"md"\|"lg"` (default `md`) · `leading?: ReactNode` · `loading?: boolean` · `disabled?: boolean` · `disabledReason?: string` (use with `disabled` to keep it focusable + tooltip-explained via `aria-disabled` instead of native `disabled`) · `kbd?: string` (trailing shortcut chip, e.g. `"⌘↵"`) · plus native `<button>` props | `danger` is ghost at rest, red only on hover. |
| **IconButton** | `size?: 24\|28\|32` (default `28`) · `icon: ReactNode` · `label: string` **(required — becomes the tooltip and `aria-label`)** · plus native `<button>` props | Always ghost. 28px size gets an 8px hit-area bleed. |
| **TextField** | `variant?: "url"\|"search"\|"plain"` (default `plain`) · `invalid?: boolean` · `errorMessage?: string` · `leading?: ReactNode` · `trailing?: ReactNode` · `copyable?: boolean` (renders a copy-to-clipboard button instead of `trailing`) · `label?: string` · plus native `<input>` props (`value`, `onChange`, `readOnly`, ...) | `url` variant is 44px/mono. Caller decides *when* to set `invalid` (DESIGN.md: never red mid-typing). |
| **Segmented** | `options: {value: string; label: string}[]` · `value: string` · `onChange: (value: string) => void` · `label: string` (accessible name) · `disabled?: boolean` · `disabledReason?: string` | `role="radiogroup"`. Used for Quality. |
| **Select** | `options: {value: string; label: string}[]` · `value: string` · `onChange: (value: string) => void` · `label: string` · `placeholder?: string` · `disabled?: boolean` | Custom listbox (Coordinator ruling — WebKitGTK's native `<select>` ignores the app theme on Linux). Full `role="listbox"`/`role="option"`, `aria-activedescendant`, arrow/Home/End/Escape/type-ahead, focus returns to the trigger on close. |
| **Checkbox** | `label: string` (required, is the click target) · plus native `<input type="checkbox">` props | For submit-time options (applied when the form submits). |
| **Switch** | `label: string` · plus native `<input type="checkbox">` props | Settings-only — implies an immediate side effect. Do not use for form options; use `Checkbox`. |
| **StatusPill** | `status: DisplayStatus` (from `lib`: `"pending"\|"running"\|"succeeded"\|"failed"\|"canceled"\|"canceling"`) · `errorCode?: ErrorCode \| null` (accepted for convenience/documentation; rendering only keys off `status`) · `dense?: boolean` (16px, for Library rows) | Derive `status` with `lib`'s `displayStatus(transcription, canceling?)` — don't re-derive the `canceling` branching at the call site. Glyph + label always both render; motion only on `running`. |
| **ProgressBar** | `variant?: "lane"\|"bar"` (default `lane`) · `mode: "determinate"\|"indeterminate"\|"stale"\|"complete"\|"failed"` · `value?: number` (0–100) · `label: string` (accessible name, e.g. the row's title) | Always render this, even at 0% — never conditionally mount/unmount it (DESIGN.md's anti-jump rule). `aria-live="off"`; the value is available via `aria-valuenow` on demand. |
| **Row** | `variant: "queue"\|"library"` · `status: DisplayStatus` · `selected?: boolean` · `flash?: boolean` (one-shot; toggle back off after 900ms to allow replay) · `stale?: boolean` · `expanded?: boolean` · `expandedContent?: ReactNode` · `thumb/title/meta/statusSlot/metric/primaryAction/secondaryActions/lane: ReactNode` · plus native `<li>` props (`onClick`, `onContextMenu`, `aria-labelledby`, `aria-describedby`, ...) | Fixed height (64px queue / 56px library) in every status — put your `ProgressBar` in the `lane` slot and keep it mounted always. `secondaryActions` only shows on hover/focus-within; `primaryAction` is always visible. |
| **Panel** | `noPadding?: boolean` · plus native `<div>` props | `--bg-surface` card. No nested Panels. |
| **SectionHeader** | `children: ReactNode` · `action?: ReactNode` · `sticky?: boolean` | 28px uppercase section label, e.g. "ACTIVE · 2 downloading, 1 queued". |
| **useToast** (hook) | returns `{ show(toast: {message: string; action?: {label: string; onClick: () => void}}): string; dismiss(id: string): void }` | Only for results the user can't otherwise see — never for a row's own error or a download completing. |
| **ToastViewport** | no props | Mount exactly once (already mounted by `App.tsx`); do not mount a second one from a feature screen. |
| **Dialog** | `open: boolean` · `onClose: () => void` · `title: string` · `children: ReactNode` · `footer?: ReactNode` · `size?: "sm"\|"lg"` (default `sm`, 480px; `lg` is 560px) · `initialFocusRef?: RefObject<HTMLElement>` | Focus-trapped, Esc closes. Mark the safe/default action's element with `data-dialog-safe-action` (or pass `initialFocusRef`) so it gets initial focus, never the destructive one. |
| **Tooltip** | `content: string` · `children: ReactElement` (single interactive element) · `side?: "top"\|"bottom"\|"left"\|"right"` (default `top`) | 400ms open delay (0ms if another tooltip is already open), no delay on keyboard focus. |
| **Spinner** | `size?: 12\|16\|24` (default `16`) · `color?: string` | Rotates normally; under `prefers-reduced-motion` it automatically switches to a non-rotational opacity pulse — no prop needed for that. |
| **Skeleton** | `width?: string\|number` · `height?: string\|number` (default `12`) | 1200ms opacity pulse; static under reduced motion. |
| **Menu** | `items: {id: string; label: string; onSelect: () => void; destructive?: boolean; disabled?: boolean}[]` · `open: boolean` · `onClose: () => void` · `anchorRef?: RefObject<HTMLElement>` (dropdown, anchored below it) · `position?: {x: number; y: number}` (context menu, anchored at a point) · `returnFocusRef?: RefObject<HTMLElement>` | `role="menu"`. Pair `position`/`returnFocusRef` with the `useContextMenu()` hook (also exported from this module) for right-click support on a `Row`. |
| **useContextMenu** (hook) | returns `{ open, position, onContextMenu, close }` | Spread `onContextMenu` onto the row; render `<Menu open={open} position={position} onClose={close} items={...} />` alongside it. |
| **EmptyState** | `variant?: "surface"\|"inline"` (default `surface`) · `headline?: string` (Instrument Serif — ONLY for first-run and cold-start, per DESIGN.md §5) · `headlineSize?: "xl"\|"lg"` · `body?: ReactNode` · `actions?: ReactNode` · `children?: ReactNode` | `inline` is the one-line "Queue is clear. 128 items in Library →" style state. |

## `src/lib`

Types (`lib/types.ts`): `Status`, `ClientStatus`, `DisplayStatus`, `Task`,
`TranscriptFormat`, `ErrorCode`, `Options`, `Audio`, `Word`, `Segment`,
`Result`, `Transcription`, `AudioSource`, `SubmitRequest`, `ListFilter`,
`ListPage`, `MediaLocation`, `CheckResult`, `HealthReport`,
`ServerSettings`, plus the `displayStatus(transcription, canceling?)`
helper that turns a `Transcription` into the `DisplayStatus` a `StatusPill`
wants (this is the one and only place the client-only `"canceling"` state
gets folded in — call it, don't re-derive it).

There is no `lib/events.ts` and no `useIngestions()` here — this service
has no `/api/v1/events` SSE endpoint. The live, app-wide cache is
`useTranscriptions()` below, kept in sync by **polling**, not events.

| Export | Signature | Description |
|---|---|---|
| `submit` | `(source: AudioSource, options: Options) => Promise<Transcription>` | Wraps `Bound.Submit`. `source` is `{kind: "path"; path; name}` (Wails, from `ChooseAudioFile()` or a dropped OS file) or `{kind: "file"; file: File}` (a browser tab's file picker/drop, uploaded as multipart bytes) — see `AudioSource`'s own doc; each adapter rejects the shape it cannot handle. Throws `HeliosError`. |
| `chooseAudioFile` | `() => Promise<string>` | Wraps `Bound.ChooseAudioFile`. Native OS file dialog; `""` if canceled. Rejects `"media_unavailable"` in a browser tab (no such dialog) — its Composer uses a plain `<input type="file">`/drop instead, never this function. |
| `list` | `(filter?: ListFilter) => Promise<ListPage>` | Wraps `Bound.List`. Use this directly for Library's filtered/paginated view; `useTranscriptions()` below is a *different*, unfiltered live cache. |
| `get` | `(id: string) => Promise<Transcription>` | Wraps `Bound.Get`. |
| `retry` | `(id: string) => Promise<Transcription>` | Wraps `Bound.Retry`. |
| `cancel` | `(id: string) => Promise<void>` | Wraps `Bound.Cancel`. |
| `deleteTranscription` | `(id: string) => Promise<void>` | Wraps `Bound.Delete` (named to avoid shadowing the `delete` keyword). |
| `health` | `() => Promise<HealthReport>` | Wraps `Bound.Health`. Prefer `useConnection()` over calling this directly — it already polls and backs off. |
| `transcript` | `(id: string, format: TranscriptFormat) => Promise<string>` | Wraps `Bound.Transcript`. The rendered transcript (txt/srt/vtt/json) as text, for the Library detail panel's format switcher and Copy-to-clipboard. Only legal once `status: "succeeded"`. |
| `saveTranscript` | `(id: string, format: TranscriptFormat, destDir: string) => Promise<string>` | Wraps `Bound.SaveTranscript`. Writes `<destDir>/<audio stem>-transcription.<format>` and returns the path written; rejects `"media_unavailable"` in a browser tab (no arbitrary local folder to write into). |
| `audioLocation` | `(id: string) => Promise<MediaLocation>` (`{path?: string; exists: boolean}`) | Wraps `Bound.AudioLocation`. Reports whether this process already holds a locally *cached* copy of the source audio (`desktop/reveal.go`'s own cache under `os.UserCacheDir()`). `openAudio`/`revealInFileManager` work either way — the Go binding downloads and caches the file first when it isn't already there — so use this only to label the action ("Open" vs "Download and open"), never to gate/disable it. |
| `openAudio` | `(id: string) => Promise<void>` | Wraps `Bound.OpenAudio`. Opens the source audio with the OS default handler, downloading it into a local cache first if needed. Rejects `"media_unavailable"` in a browser tab. |
| `revealInFileManager` | `(id: string) => Promise<void>` | Wraps `Bound.RevealInFileManager`. Same caching behaviour and `"media_unavailable"` case as `openAudio`. |
| `chooseSaveDirectory` | `() => Promise<string>` | Wraps `Bound.ChooseSaveDirectory`. Native "choose a folder" dialog for `saveTranscript`'s `destDir`; `""` if canceled. Rejects `"media_unavailable"` in a browser tab. |
| `defaultSaveDirectory` | `() => Promise<string>` | Wraps `Bound.DefaultSaveDirectory`. The user's system downloads folder, or `""` (never a rejection) when there is none to suggest. |
| `HeliosError` | `class extends Error { code: ErrorCode \| "unknown"; details?: Record<string, unknown> }` | What every rejected call above throws. Key UI behaviour (e.g. whether to offer Retry) off `.code`, never `.message`. |
| `parseError` | `(err: unknown) => HeliosError` | Used internally by `lib/wails/api.ts`; exported in case you need to parse an error from somewhere else (there should be nowhere else). |
| `useTranscriptions` | `() => {transcriptions: Transcription[]; loading: boolean; error?: HeliosError; lastSyncAt?: number}` | The live, polling-updated cache (first 200 items, `createdAt` desc). Single app-wide singleton — mounting this in both screens shares one subscription, not two. Polls every 1.5s while any known transcription is `pending`/`running`, every 10s otherwise (see `lib/store.ts`'s own doc). **Not paginated** — Library's own filtered/paginated view should call `list()` directly instead. |
| `transcriptionStore` | singleton with `.refreshNow()` | The store behind `useTranscriptions`; call `.refreshNow()` right after an action (submit/cancel/retry/delete) that just changed server state, so the UI reflects it without waiting out the current poll interval. |
| `useConnection` | `() => ConnectionState` (`{status: "connecting"\|"online"\|"reconnecting"\|"offline"; attempt: number; retryDelayMs: number; retryAt?: number; report?: HealthReport; lastError?: HeliosError; lastCheckedAt?: number; lastOkAt?: number}`) | Polls `Health()` (10s while online) with 1s→30s exponential backoff while failing. `"offline"` = never once reachable (cold start); `"reconnecting"` = was reachable, now isn't. |
| `connectionMonitor` | singleton with `.retryNow()` | The store behind `useConnection`; call `.retryNow()` from a "Retry now" button to short-circuit the backoff timer. |
| `formatBytes` | `(bytes: number) => string` | SI units (MB = 10⁶), matches a Linux file manager. |
| `formatDuration` | `(seconds?: number \| null) => string` | `m:ss` / `h:mm:ss`; `—` for `0`/absent — a media *length* of zero means "not yet known", never `0:00`. |
| `formatTimestamp` | `(seconds: number) => string` | `m:ss` / `h:mm:ss`, including a genuine `0:00` — for a *position* into the audio (a segment/word's start/end), used by the transcript viewer's clickable `[mm:ss]` timestamps. |
| `formatRelative` | `(iso?: string \| null, now?: Date) => string` | "2 minutes ago". Pair with `formatAbsolute` for the tooltip/`title`. |
| `formatAbsolute` | `(iso?: string \| null) => string` | Locale absolute date/time string. |
| `secondsSince` | `(iso?: string \| null, now?: Date) => number \| undefined` | For "last update 14s ago" staleness displays and a running job's elapsed time. |
| `useTheme` | `() => [ThemePreference, (p: ThemePreference) => void]` (`ThemePreference = "system"\|"light"\|"dark"`) | Persists to `localStorage`, stamps `data-theme` on `<html>`. First-run default is `"dark"`, not `"system"`. Already wired into `App.tsx`'s toolbar toggle and Settings; call it again anywhere you need to read/change the preference, it's a shared singleton. |

### The one rule the store enforces for you

`transcriptionStore` polls `list()` on a timer instead of reacting to
events — 1.5s while any known transcription is `pending`/`running`, 10s
otherwise, re-checked after every refresh. Do not build a second,
independent polling cache in a feature screen — consume this one (or call
`list()`/`get()` fresh yourself for a filtered/paginated view, same as
Library does), and call `transcriptionStore.refreshNow()` after a mutating
action instead of adding your own timer.
