package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/httpclient"
	"github.com/pablofornell/helios-audio-transcription-service/internal/transcript"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// withCacheDir points os.UserCacheDir() at a fresh temp directory for the
// duration of a test, via XDG_CACHE_HOME (os.UserCacheDir's own source on
// Linux) — so audioCacheDir's output is both deterministic and cleaned up
// automatically by t.TempDir.
func withCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	return dir
}

func TestAudioCacheDir(t *testing.T) {
	cacheHome := withCacheDir(t)

	dir, err := audioCacheDir("t-1")
	if err != nil {
		t.Fatalf("audioCacheDir: %v", err)
	}
	want := filepath.Join(cacheHome, audioCacheAppDir, audioCacheSubdir, "t-1")
	if dir != want {
		t.Errorf("audioCacheDir = %q, want %q", dir, want)
	}
}

func TestCachedAudio(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.wav")

	if cachedAudio(path, 5) {
		t.Error("cachedAudio reported a non-existent file as cached")
	}

	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !cachedAudio(path, 5) {
		t.Error("cachedAudio reported an existing, correctly-sized file as not cached")
	}
	if cachedAudio(path, 999) {
		t.Error("cachedAudio reported a wrong-sized file as cached")
	}
}

// TestApp_AudioLocation_NotCached proves AudioLocation never downloads —
// only fakeAPI.getFunc is configured, and Get is enough to answer "no,
// nothing is cached yet".
func TestApp_AudioLocation_NotCached(t *testing.T) {
	withCacheDir(t)
	api := &fakeAPI{
		getFunc: func(ctx context.Context, id string) (*domain.Transcription, error) {
			t := transcriptionFor(id)
			t.Audio.SizeBytes = 5
			return t, nil
		},
	}
	a := &App{api: api}

	got, err := a.AudioLocation("t-1")
	if err != nil {
		t.Fatalf("AudioLocation: %v", err)
	}
	if got.Exists {
		t.Errorf("AudioLocation reported Exists = true with nothing downloaded")
	}
}

// TestApp_AudioLocation_Cached proves AudioLocation reports a warm cache
// once ensureLocalAudio (via OpenAudio) has populated it.
func TestApp_AudioLocation_Cached(t *testing.T) {
	withCacheDir(t)
	body := []byte("RIFF fake wav")
	tr := transcriptionFor("t-1")
	tr.Audio.SizeBytes = int64(len(body))

	origXDGOpen := xdgOpen
	defer func() { xdgOpen = origXDGOpen }()
	xdgOpen = func(path string) error { return nil }

	api := &fakeAPI{
		getFunc: func(ctx context.Context, id string) (*domain.Transcription, error) {
			return tr, nil
		},
		openAudioFunc: func(ctx context.Context, id string) (io.ReadSeekCloser, httpclient.AudioInfo, error) {
			return nopCloseReadSeeker{bytes.NewReader(body)}, httpclient.AudioInfo{}, nil
		},
	}
	a := &App{api: api}

	if err := a.OpenAudio("t-1"); err != nil {
		t.Fatalf("OpenAudio: %v", err)
	}

	got, err := a.AudioLocation("t-1")
	if err != nil {
		t.Fatalf("AudioLocation: %v", err)
	}
	if !got.Exists {
		t.Fatalf("AudioLocation reported Exists = false after OpenAudio cached it")
	}
	data, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", got.Path, err)
	}
	if !bytes.Equal(data, body) {
		t.Errorf("cached file content = %q, want %q", data, body)
	}
}

// TestApp_OpenAudio_DownloadsOnce proves a second OpenAudio call reuses
// the cache rather than downloading again.
func TestApp_OpenAudio_DownloadsOnce(t *testing.T) {
	withCacheDir(t)
	body := []byte("RIFF fake wav")
	tr := transcriptionFor("t-1")
	tr.Audio.SizeBytes = int64(len(body))

	origXDGOpen := xdgOpen
	defer func() { xdgOpen = origXDGOpen }()
	var openedPaths []string
	xdgOpen = func(path string) error {
		openedPaths = append(openedPaths, path)
		return nil
	}

	downloads := 0
	api := &fakeAPI{
		getFunc: func(ctx context.Context, id string) (*domain.Transcription, error) {
			return tr, nil
		},
		openAudioFunc: func(ctx context.Context, id string) (io.ReadSeekCloser, httpclient.AudioInfo, error) {
			downloads++
			return nopCloseReadSeeker{bytes.NewReader(body)}, httpclient.AudioInfo{}, nil
		},
	}
	a := &App{api: api}

	if err := a.OpenAudio("t-1"); err != nil {
		t.Fatalf("OpenAudio (1): %v", err)
	}
	if err := a.OpenAudio("t-1"); err != nil {
		t.Fatalf("OpenAudio (2): %v", err)
	}
	if downloads != 1 {
		t.Errorf("OpenAudio downloaded %d times, want 1 (second call should hit the cache)", downloads)
	}
	if len(openedPaths) != 2 || openedPaths[0] != openedPaths[1] {
		t.Errorf("xdgOpen was called with %v, want the same path twice", openedPaths)
	}
}

// TestApp_RevealInFileManager_OpensContainingDir proves
// RevealInFileManager launches the cached file's parent directory, not
// the file itself.
func TestApp_RevealInFileManager_OpensContainingDir(t *testing.T) {
	withCacheDir(t)
	body := []byte("data")
	tr := transcriptionFor("t-1")
	tr.Audio.SizeBytes = int64(len(body))

	origXDGOpen := xdgOpen
	defer func() { xdgOpen = origXDGOpen }()
	var gotPath string
	xdgOpen = func(path string) error {
		gotPath = path
		return nil
	}

	api := &fakeAPI{
		getFunc: func(ctx context.Context, id string) (*domain.Transcription, error) { return tr, nil },
		openAudioFunc: func(ctx context.Context, id string) (io.ReadSeekCloser, httpclient.AudioInfo, error) {
			return nopCloseReadSeeker{bytes.NewReader(body)}, httpclient.AudioInfo{}, nil
		},
	}
	a := &App{api: api}

	if err := a.RevealInFileManager("t-1"); err != nil {
		t.Fatalf("RevealInFileManager: %v", err)
	}
	info, err := os.Stat(gotPath)
	if err != nil || !info.IsDir() {
		t.Errorf("RevealInFileManager opened %q, want an existing directory", gotPath)
	}
}

func TestWriteUniqueFile(t *testing.T) {
	dir := t.TempDir()

	first, err := writeUniqueFile(dir, "transcript.txt", bytes.NewReader([]byte("first")))
	if err != nil {
		t.Fatalf("writeUniqueFile (1): %v", err)
	}
	if filepath.Base(first) != "transcript.txt" {
		t.Errorf("first write landed at %q, want plain transcript.txt", first)
	}

	second, err := writeUniqueFile(dir, "transcript.txt", bytes.NewReader([]byte("second")))
	if err != nil {
		t.Fatalf("writeUniqueFile (2): %v", err)
	}
	if second == first {
		t.Fatalf("second write reused the same path as the first — it must not overwrite")
	}
	if filepath.Base(second) != "transcript (2).txt" {
		t.Errorf("second write landed at %q, want %q", filepath.Base(second), "transcript (2).txt")
	}

	gotFirst, _ := os.ReadFile(first)
	gotSecond, _ := os.ReadFile(second)
	if string(gotFirst) != "first" || string(gotSecond) != "second" {
		t.Errorf("file contents were mixed up: first=%q second=%q", gotFirst, gotSecond)
	}
}

func TestApp_Transcript(t *testing.T) {
	var gotID string
	var gotFormat transcript.Format
	api := &fakeAPI{
		getTranscriptFunc: func(ctx context.Context, id string, format transcript.Format) ([]byte, error) {
			gotID, gotFormat = id, format
			return []byte("hello world"), nil
		},
	}
	a := &App{api: api}

	got, err := a.Transcript("t-1", "txt")
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if got != "hello world" {
		t.Errorf("Transcript = %q, want %q", got, "hello world")
	}
	if gotID != "t-1" || gotFormat != transcript.FormatText {
		t.Errorf("GetTranscript called with (%q, %q)", gotID, gotFormat)
	}
}

func TestApp_Transcript_RejectsInvalidFormat(t *testing.T) {
	a := &App{api: &fakeAPI{}}

	_, err := a.Transcript("t-1", "pdf")
	if domain.CodeOf(err) != domain.ErrCodeInvalidInput {
		t.Errorf("Transcript error code = %q, want %q", domain.CodeOf(err), domain.ErrCodeInvalidInput)
	}
}

func TestApp_SaveTranscript(t *testing.T) {
	destDir := t.TempDir()
	tr := transcriptionFor("t-1")
	tr.Audio.Filename = "My Interview.m4a"

	api := &fakeAPI{
		getFunc: func(ctx context.Context, id string) (*domain.Transcription, error) { return tr, nil },
		getTranscriptFunc: func(ctx context.Context, id string, format transcript.Format) ([]byte, error) {
			if format != transcript.FormatSRT {
				t.Errorf("GetTranscript called with format %q, want %q", format, transcript.FormatSRT)
			}
			return []byte("1\n00:00:00,000 --> 00:00:01,000\nHello\n"), nil
		},
	}
	a := &App{api: api}

	path, err := a.SaveTranscript("t-1", "srt", destDir)
	if err != nil {
		t.Fatalf("SaveTranscript: %v", err)
	}
	wantName := "My Interview-transcription.srt"
	if filepath.Base(path) != wantName {
		t.Errorf("SaveTranscript wrote %q, want basename %q", path, wantName)
	}
	if filepath.Dir(path) != destDir {
		t.Errorf("SaveTranscript wrote into %q, want %q", filepath.Dir(path), destDir)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "1\n00:00:00,000 --> 00:00:01,000\nHello\n" {
		t.Errorf("SaveTranscript wrote unexpected content: %q", data)
	}
}

func TestApp_DefaultSaveDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DOWNLOAD_DIR", dir)

	a := &App{}
	if got := a.DefaultSaveDirectory(); got != dir {
		t.Errorf("DefaultSaveDirectory = %q, want %q", got, dir)
	}
}

// TestApp_ChooseSaveDirectory proves ChooseSaveDirectory calls through
// openDirectoryDialog and returns its result unchanged.
func TestApp_ChooseSaveDirectory(t *testing.T) {
	orig := openDirectoryDialog
	defer func() { openDirectoryDialog = orig }()

	openDirectoryDialog = func(ctx context.Context, opts runtime.OpenDialogOptions) (string, error) {
		return "/home/user/Music/Transcripts", nil
	}

	a := &App{}
	got, err := a.ChooseSaveDirectory()
	if err != nil {
		t.Fatalf("ChooseSaveDirectory: %v", err)
	}
	if got != "/home/user/Music/Transcripts" {
		t.Errorf("ChooseSaveDirectory = %q, want %q", got, "/home/user/Music/Transcripts")
	}
}
