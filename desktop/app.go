package main

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/httpclient"
)

// App is the Wails binding surface: every method on it that Wails' AST
// scanner finds exported becomes a callable the frontend can await. It
// holds the one transcriptionAPI this process talks to (see mode.go's
// newAdapter) plus the Wails runtime context OnStartup hands it.
//
// Unlike the sibling ingestion service's own App, there is no adapter
// teardown to run on quit (a *httpclient.Client owns no resource that
// needs closing) and no event-forwarding goroutine to start and stop (this
// service has no SSE endpoint at all — the frontend's own store polls
// List instead, per docs/DESKTOP.md's "no SSE" decision). That is what
// lets this App skip OnShutdown, and the eventsMu/shuttingDown/events
// machinery the sibling's own App needed to make restarting that
// goroutine safe, entirely.
//
// No business logic lives here, mirroring internal/httpapi's own rule:
// every method below does nothing but translate a call from the frontend
// into a call on api and its result (or *domain.Error) back into a DTO.
type App struct {
	api transcriptionAPI

	// server is adapter.server, set once by NewApp: this process' live
	// connection target. GetServerSettings/SetServerSettings/
	// TestServerConnection (serversettings.go) are the only methods that
	// read it.
	server *serverManager

	// mu guards ctx below. Wails' own doc for OnStartup reads as though
	// the frontend cannot issue a call until OnStartup returns, but the
	// Linux frontend actually starts `go OnStartup(ctx)` and then runs the
	// window concurrently (see wailsapp/wails/v2/internal/frontend/
	// desktop/linux.Frontend.Run) — a bound call can land before
	// OnStartup's write to ctx below is visible to the goroutine handling
	// it, which is a real, -race-detected data race, not a hypothetical
	// one. See the sibling ingestion service's own App.mu doc, which
	// found this the hard way.
	mu sync.RWMutex

	// ctx is the Wails runtime context, set once by OnStartup and read by
	// every bound method thereafter (via runtimeCtx). It is nil until
	// OnStartup runs; a call that lands before that write is visible reads
	// nil rather than racing.
	ctx context.Context
}

// NewApp returns an App bound to ad. Called once, before wails.Run, from
// main — see mode.go's newAdapter for how ad is built.
func NewApp(ad *adapter) *App {
	return &App{api: ad.api, server: ad.server}
}

// OnStartup is registered as options.App.OnStartup. It saves the Wails
// runtime context every other method needs. See mu's doc on why the write
// is synchronised.
func (a *App) OnStartup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()
}

// runtimeCtx returns the Wails runtime context OnStartup saved, or nil if
// OnStartup has not run (or its write is not yet visible to this
// goroutine) — see mu's doc. Every bound method reads a.ctx through this,
// never directly.
func (a *App) runtimeCtx() context.Context {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ctx
}

// audioFileFilterPattern lists the extensions ChooseAudioFile's native
// dialog offers by default — every container whisper.cpp's own surrounding
// tooling (ffmpeg, in practice) can demux an audio track from, audio and
// video alike, since a video file with an audio track is exactly as valid
// an upload as a bare audio one (see internal/audio's probe/normalize
// step, which never rejects a file merely for carrying video).
const audioFileFilterPattern = "*.mp3;*.wav;*.m4a;*.aac;*.flac;*.ogg;*.opus;*.wma;*.mp4;*.mov;*.mkv;*.webm;*.avi"

// openFileDialog is runtime.OpenFileDialog, indirected through a package
// variable for the same reason reveal.go's openDirectoryDialog is: it
// reaches into the real Wails runtime given only a ctx, which no test in
// this package can supply.
var openFileDialog = runtime.OpenFileDialog

// ChooseAudioFile prompts the user, via the desktop's own native open
// dialog, to pick a local audio or video file for Submit, and returns its
// absolute path. A canceled dialog is not an error: like
// runtime.OpenFileDialog itself, this returns ("", nil) in that case, and
// the frontend must treat an empty path as "the user changed their mind".
func (a *App) ChooseAudioFile() (string, error) {
	path, err := openFileDialog(a.runtimeCtx(), runtime.OpenDialogOptions{
		Title: "Choose an audio or video file to transcribe",
		Filters: []runtime.FileFilter{
			{DisplayName: "Audio and video files", Pattern: audioFileFilterPattern},
			{DisplayName: "All files", Pattern: "*.*"},
		},
	})
	if err != nil {
		return "", domain.Wrap(domain.ErrCodeInternal, "open file picker", err)
	}
	return path, nil
}

// mediaTypeForFile guesses a local file's Content-Type from its extension,
// the same fallback internal/httpapi's own mediaTypeForPart applies once a
// multipart part carries no Content-Type header of its own — there is no
// header to read at all for a plain local file, so this is Submit's only
// source for one. "" (never sent) becomes application/octet-stream
// server-side regardless, so returning that directly here, rather than
// omitting it, changes nothing about how the server behaves; it does let
// GetTranscript's own safeAudioContentType-style downgrade logic start
// from a real guess instead of always falling straight through to it.
func mediaTypeForFile(path string) string {
	if mt := mime.TypeByExtension(filepath.Ext(path)); mt != "" {
		return mt
	}
	return "application/octet-stream"
}

// Submit opens req.FilePath and uploads it as a new transcription. See
// transcriptionAPI.Submit. A FilePath that cannot be opened (moved,
// deleted, or never existed — the file the user picked is not locked or
// spooled anywhere between ChooseAudioFile returning and Submit being
// called) is domain.ErrCodeInvalidInput, not domain.ErrCodeInternal: it is
// exactly as much "bad input" as a malformed Options would be.
func (a *App) Submit(req SubmitRequestDTO) (TranscriptionDTO, error) {
	f, err := os.Open(req.FilePath)
	if err != nil {
		return TranscriptionDTO{}, domain.Wrap(domain.ErrCodeInvalidInput, fmt.Sprintf("open %s", req.FilePath), err)
	}
	defer f.Close()

	t, err := a.api.Submit(a.runtimeCtx(), httpclient.SubmitRequest{
		Filename:  filepath.Base(req.FilePath),
		MediaType: mediaTypeForFile(req.FilePath),
		Options:   req.Options.toDomain(),
		Body:      f,
	})
	if err != nil {
		return TranscriptionDTO{}, err
	}
	return transcriptionFromDomain(t), nil
}

// List returns a page of transcriptions matching filter. See
// transcriptionAPI.List.
func (a *App) List(filter ListFilterDTO) (ListPageDTO, error) {
	f, err := filter.toDomain()
	if err != nil {
		return ListPageDTO{}, err
	}
	page, err := a.api.List(a.runtimeCtx(), f)
	if err != nil {
		return ListPageDTO{}, err
	}
	return listPageFromDomain(page), nil
}

// Get returns the transcription with the given id. See
// transcriptionAPI.Get.
func (a *App) Get(id string) (TranscriptionDTO, error) {
	t, err := a.api.Get(a.runtimeCtx(), id)
	if err != nil {
		return TranscriptionDTO{}, err
	}
	return transcriptionFromDomain(t), nil
}

// Retry re-queues a failed or canceled transcription. See
// transcriptionAPI.Retry.
func (a *App) Retry(id string) (TranscriptionDTO, error) {
	t, err := a.api.Retry(a.runtimeCtx(), id)
	if err != nil {
		return TranscriptionDTO{}, err
	}
	return transcriptionFromDomain(t), nil
}

// Cancel requests that a pending or running transcription stop. See
// transcriptionAPI.Cancel.
func (a *App) Cancel(id string) error {
	_, err := a.api.Cancel(a.runtimeCtx(), id)
	return err
}

// Delete removes a transcription's record and stored audio. See
// transcriptionAPI.Delete.
func (a *App) Delete(id string) error {
	return a.api.Delete(a.runtimeCtx(), id)
}

// Health reports the target server's reachability and readiness. See
// transcriptionAPI.Health: it never returns a non-nil error — even
// "unreachable" comes back as HealthReportDTO{OK: false, ...} — so the
// frontend can always render the result directly.
func (a *App) Health() (HealthReportDTO, error) {
	report, err := a.api.Health(a.runtimeCtx())
	if err != nil {
		return HealthReportDTO{}, err
	}
	return healthReportDTOFromReport(report), nil
}
