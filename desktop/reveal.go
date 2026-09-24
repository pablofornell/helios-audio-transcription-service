package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/transcript"
)

// This file adapts the sibling ingestion service's own reveal.go from
// "media file this process might already see on the server's own disk"
// (via a shared HELIOS_MEDIA_ROOT mount) to "audio file this process
// fetches from a remote helios-transcription-api and keeps a local cache
// of". The two services differ in a way that matters here:
// domain.Transcription's SourcePath (the store-relative path of the
// uploaded original) is deliberately `json:"-"` — internal/httpapi never
// serves it, and there is no bind-mount convention documented for this
// service the way HELIOS_MEDIA_ROOT was for the ingestion one — so this
// desktop process has no way to open the server's own copy directly, ever.
// Every byte OpenAudio/RevealInFileManager ever show the user came from
// GET .../audio (via transcriptionAPI.OpenAudio), spooled once into a
// per-transcription cache file under os.UserCacheDir() and reused after
// that; AudioLocation reports whether that cache is already warm, without
// itself triggering a fetch.
//
// SaveTranscript is this file's other job: fetching a rendered transcript
// (transcriptionAPI.GetTranscript) and writing it to a user-chosen
// directory, reusing sanitizeTitle (sanitize.go) and the same
// collision-safe, never-partial write idiom the sibling's SaveCopy used
// for media.

// audioCacheAppDir and audioCacheSubdir make up the path this process
// caches downloaded audio under: os.UserCacheDir()/helios-ats/audio/<id>/
// <sanitized filename> (e.g. ~/.cache/helios-ats/audio/... on Linux).
// Scoped per transcription id, rather than one flat directory, so
// RevealInFileManager's "open the containing folder" always shows exactly
// one file — this cache's own copy — never a mix of unrelated cached
// audio.
const audioCacheAppDir = "helios-ats"
const audioCacheSubdir = "audio"

// xdgOpen launches path (a file or a directory) with xdg-open. It is a
// package variable, not a plain function, so tests can replace it and
// assert what would have been launched without ever actually launching a
// file manager.
var xdgOpen = defaultXDGOpen

// defaultXDGOpen is xdgOpen's real implementation: it asks the desktop
// environment's default handler to open path, via xdg-open, without
// waiting for whatever application xdg-open launches to exit — only for
// xdg-open itself to hand off — so this call cannot block the frontend for
// as long as, say, a media player stays open.
func defaultXDGOpen(path string) error {
	cmd := exec.Command("xdg-open", path)
	if err := cmd.Start(); err != nil {
		return domain.Wrap(domain.ErrCodeInternal, fmt.Sprintf("launch xdg-open for %s", path), err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// audioCacheDir returns the directory id's cached audio download lives
// in — never the file itself, since audioFilePath's own filename component
// depends on the transcription's Audio.Filename, which callers must have
// already fetched.
func audioCacheDir(id string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", domain.Wrap(domain.ErrCodeInternal, "find user cache directory", err)
	}
	return filepath.Join(base, audioCacheAppDir, audioCacheSubdir, id), nil
}

// audioFilePath returns the full path this process caches t's audio
// download at.
func audioFilePath(t *domain.Transcription) (string, error) {
	dir, err := audioCacheDir(t.ID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sanitizeTitle(t.Audio.Filename)), nil
}

// cachedAudio reports whether path already holds t's audio: present, and
// at least the same size as t.Audio.SizeBytes reports (the same
// size-not-hash trade-off the sibling's localMediaFile made, for the same
// reason — this runs on every AudioLocation/OpenAudio/RevealInFileManager
// call, not in a background job, and hashing a large audio file on every
// call would make each noticeably slow; a stale, wrong-size leftover from
// an earlier, killed-mid-download attempt is exactly what a size mismatch
// does catch).
func cachedAudio(path string, wantSize int64) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Size() == wantSize
}

// AudioLocation reports whether this process already holds a locally
// cached copy of id's audio file, and if so, its real, absolute path. It
// never fetches anything itself — see ensureLocalAudio, which
// OpenAudio/RevealInFileManager call instead — so the frontend can use it
// to decide how to label those actions (e.g. "Open" vs. "Download and
// open") without triggering a download just to ask.
func (a *App) AudioLocation(id string) (MediaLocationDTO, error) {
	t, err := a.api.Get(a.runtimeCtx(), id)
	if err != nil {
		return MediaLocationDTO{}, err
	}
	path, err := audioFilePath(t)
	if err != nil {
		return MediaLocationDTO{}, err
	}
	if !cachedAudio(path, t.Audio.SizeBytes) {
		return MediaLocationDTO{Exists: false}, nil
	}
	return MediaLocationDTO{Path: path, Exists: true}, nil
}

// ensureLocalAudio returns the real, local, absolute path to id's audio
// file, downloading it via transcriptionAPI.OpenAudio into this process's
// cache first if it is not already there (or the cached copy's size no
// longer matches, e.g. a previous download was interrupted). Both
// OpenAudio and RevealInFileManager call this instead of ever assuming
// direct filesystem access to the server's own storage — see this file's
// own package doc.
func (a *App) ensureLocalAudio(ctx context.Context, id string) (string, error) {
	t, err := a.api.Get(ctx, id)
	if err != nil {
		return "", err
	}
	path, err := audioFilePath(t)
	if err != nil {
		return "", err
	}
	if cachedAudio(path, t.Audio.SizeBytes) {
		return path, nil
	}
	if err := a.downloadAudioTo(ctx, id, path); err != nil {
		return "", err
	}
	logInfof(ctx, "desktop: cached audio for transcription %s at %q", id, path)
	return path, nil
}

// downloadAudioTo streams id's audio (via transcriptionAPI.OpenAudio) into
// a temp file inside filepath.Dir(dest) and renames it into place once
// complete, so a reader that fails partway through (a dropped connection,
// a canceled ctx) never leaves a truncated file sitting at dest — the same
// temp-file-then-rename guarantee the sibling's saveCopyLink gave a single
// SaveCopy file, simplified because dest's name is fixed per transcription
// (there is nothing to uniquify: a fresh download simply replaces whatever
// stale copy, if any, is already cached there).
func (a *App) downloadAudioTo(ctx context.Context, id, dest string) error {
	reader, _, err := a.api.OpenAudio(ctx, id)
	if err != nil {
		return err
	}
	defer reader.Close()

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return domain.Wrap(domain.ErrCodeInternal, fmt.Sprintf("create audio cache directory %s", dir), err)
	}

	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return domain.Wrap(domain.ErrCodeInternal, fmt.Sprintf("create temp file in %s", dir), err)
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, reader); err != nil {
		_ = tmp.Close()
		return domain.Wrap(domain.ErrCodeInternal, fmt.Sprintf("download audio for transcription %s", id), err)
	}
	if err := tmp.Close(); err != nil {
		return domain.Wrap(domain.ErrCodeInternal, fmt.Sprintf("close temp file %s", tmpPath), err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return domain.Wrap(domain.ErrCodeInternal, fmt.Sprintf("finalize cached audio at %s", dest), err)
	}
	removeTmp = false
	return nil
}

// OpenAudio opens id's audio file, in place, with the desktop's default
// handler for its type — downloading it into this process's cache first
// if it is not already there (see ensureLocalAudio).
func (a *App) OpenAudio(id string) error {
	path, err := a.ensureLocalAudio(a.runtimeCtx(), id)
	if err != nil {
		return err
	}
	return xdgOpen(path)
}

// RevealInFileManager opens the folder containing id's cached audio file —
// downloading it first if it is not already cached (see ensureLocalAudio)
// — the closest xdg-open gets to "reveal and select this file" without a
// per-desktop-environment protocol for the latter. Because
// audioCacheDir scopes each transcription to its own subdirectory, the
// folder this reveals contains exactly one file: this transcription's
// audio.
func (a *App) RevealInFileManager(id string) error {
	path, err := a.ensureLocalAudio(a.runtimeCtx(), id)
	if err != nil {
		return err
	}
	return xdgOpen(filepath.Dir(path))
}

// openDirectoryDialog is runtime.OpenDirectoryDialog, indirected through a
// package variable for the same reason xdgOpen (above) is: it reaches into
// the real Wails runtime given only a ctx, which no test in this package
// can supply.
var openDirectoryDialog = runtime.OpenDirectoryDialog

// ChooseSaveDirectory prompts the user, via the desktop's own native
// directory picker, to choose the directory SaveTranscript should write
// into, and returns the chosen absolute path. It exists so the frontend
// never has to ask the user to type an absolute path by hand for
// SaveTranscript's destDir.
//
// The picker opens at DefaultSaveDirectory's own answer, when it has one,
// so the user is not dropped into wherever the OS's own dialog defaults
// to (often the home directory) every single time.
//
// A canceled dialog is not an error: like runtime.OpenDirectoryDialog
// itself, this returns ("", nil) in that case, and the frontend must treat
// an empty path as "the user changed their mind", not as a failure to
// report.
func (a *App) ChooseSaveDirectory() (string, error) {
	opts := runtime.OpenDialogOptions{
		Title: "Choose where to save the transcript",
	}
	if def := a.DefaultSaveDirectory(); def != "" {
		opts.DefaultDirectory = def
	}
	dir, err := openDirectoryDialog(a.runtimeCtx(), opts)
	if err != nil {
		return "", domain.Wrap(domain.ErrCodeInternal, "open directory picker", err)
	}
	return dir, nil
}

// DefaultSaveDirectory returns the user's system downloads directory, as a
// ready-to-use SaveTranscript destDir, so the frontend can pre-fill the
// save dialog's destination field and ChooseSaveDirectory (above) can open
// its native picker somewhere more useful than the OS's own default.
//
// Returns "" — never an error — when systemDownloadDir cannot resolve one
// (no $HOME, an unusual/headless environment). An unresolvable default is
// not a failure the frontend needs to react to, just an empty field left
// for the user to fill in themselves.
func (a *App) DefaultSaveDirectory() string {
	dir, err := systemDownloadDir()
	if err != nil {
		return ""
	}
	return dir
}

// maxSaveAttempts bounds how many "name (N)" alternatives
// writeUniqueFile will try before giving up — a directory deliberately
// salted with "interview.txt", "interview (2).txt", ... up to this count
// cannot make that loop run forever.
const maxSaveAttempts = 10000

// writeUniqueFile copies src's remaining bytes into a temp file inside dir
// and links it into its final, unique name — name itself when that is
// free, otherwise "name (2)", "name (3)", ... up to maxSaveAttempts, the
// same convention a desktop file manager's own "keep both" uses, and the
// sibling ingestion service's saveCopyLink used for saved media. It never
// leaves a partial file behind on failure: the copy is written to a temp
// file in dir first (so the link below is within one filesystem, never
// crossing a mount) and only linked into its final name (via os.Link,
// which fails with EEXIST instead of overwriting — unlike os.Rename onto
// an existing name) once it is complete.
//
// Returns the final, possibly-uniquified path actually used.
func writeUniqueFile(dir, name string, src io.Reader) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", domain.Wrap(domain.ErrCodeInternal, fmt.Sprintf("create %s", dir), err)
	}

	tmp, err := os.CreateTemp(dir, ".helios-ats-save-*")
	if err != nil {
		return "", domain.Wrap(domain.ErrCodeInternal, fmt.Sprintf("create temp file in %s", dir), err)
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return "", domain.Wrap(domain.ErrCodeInternal, fmt.Sprintf("write %s", tmpPath), err)
	}
	if err := tmp.Close(); err != nil {
		return "", domain.Wrap(domain.ErrCodeInternal, fmt.Sprintf("close %s", tmpPath), err)
	}

	ext := filepath.Ext(name)
	base := name[:len(name)-len(ext)]
	for attempt := 1; attempt <= maxSaveAttempts; attempt++ {
		candidate := name
		if attempt > 1 {
			candidate = fmt.Sprintf("%s (%d)%s", base, attempt, ext)
		}
		dest := filepath.Join(dir, candidate)
		if err := os.Link(tmpPath, dest); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", domain.Wrap(domain.ErrCodeInternal, fmt.Sprintf("finalize %s", dest), err)
		}
		removeTmp = false
		_ = os.Remove(tmpPath)
		return dest, nil
	}
	return "", domain.New(domain.ErrCodeInternal, fmt.Sprintf("save transcript: %d existing names like %q in %s, giving up", maxSaveAttempts, name, dir))
}

// Transcript returns id's rendered transcript in the given format
// ("txt", "srt", "vtt" or "json" — see internal/transcript.Format), for
// the frontend's format switcher / Copy-to-clipboard. It returns a
// *domain.Error with domain.ErrCodeConflict (surfaced from the server)
// when id has not succeeded yet, and domain.ErrCodeInvalidInput for an
// unrecognised format — the same classification GET .../transcript itself
// uses.
func (a *App) Transcript(id, format string) (string, error) {
	f, err := transcript.ParseFormat(format)
	if err != nil {
		return "", err
	}
	body, err := a.api.GetTranscript(a.runtimeCtx(), id, f)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// SaveTranscript fetches id's rendered transcript in the given format and
// writes it to destDir, named after the transcription's own audio
// filename (stripped of its original extension, sanitised via
// sanitizeTitle, and suffixed with -transcription and the format's own
// extension), returning the real path written. destDir must already exist;
// the frontend is expected to have obtained it from the user (e.g. via
// ChooseSaveDirectory), not to have invented it. It never silently
// overwrites an existing file at the
// destination name — see writeUniqueFile.
func (a *App) SaveTranscript(id, format, destDir string) (string, error) {
	f, err := transcript.ParseFormat(format)
	if err != nil {
		return "", err
	}

	ctx := a.runtimeCtx()
	t, err := a.api.Get(ctx, id)
	if err != nil {
		return "", err
	}
	body, err := a.api.GetTranscript(ctx, id, f)
	if err != nil {
		return "", err
	}

	stem := sanitizeTitle(stripExt(t.Audio.Filename))
	name := stem + "-transcription." + string(f)
	return writeUniqueFile(destDir, name, bytes.NewReader(body))
}
