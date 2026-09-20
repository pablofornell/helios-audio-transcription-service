package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/httpclient"
)

// transcriptionFor returns a minimal, otherwise-zero *domain.Transcription
// with the given id, for tests that only care about round-tripping it
// through transcriptionFromDomain.
func transcriptionFor(id string) *domain.Transcription {
	return &domain.Transcription{
		ID:     id,
		Status: domain.StatusPending,
		Audio:  domain.Audio{Filename: "clip.wav"},
	}
}

// TestApp_OnStartupRaceWithBoundCall drives OnStartup concurrently with a
// bound call reading a.ctx (via Get), exactly the race the sibling
// ingestion service's own review found: Wails' Linux frontend runs
// `go OnStartup(ctx)` and then the window concurrently, so a bound call
// dispatched to App can land before OnStartup's write to ctx is visible to
// the goroutine handling it. Run under `go test -race`, this fails loudly
// against an unsynchronised App — both goroutines would read/write a.ctx
// with no happens-before edge between them.
func TestApp_OnStartupRaceWithBoundCall(t *testing.T) {
	for i := 0; i < 200; i++ {
		api := &fakeAPI{
			getFunc: func(ctx context.Context, id string) (*domain.Transcription, error) {
				return transcriptionFor(id), nil
			},
		}
		a := &App{api: api}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			a.OnStartup(context.Background())
		}()
		go func() {
			defer wg.Done()
			<-start
			// Get reads a.ctx through runtimeCtx, exactly as any other
			// bound method does. Whichever of nil or the real ctx it
			// observes is a valid outcome here — what must not happen is
			// an unsynchronised, torn read racing OnStartup's write.
			_, _ = a.Get("11111111-1111-1111-1111-111111111111")
		}()
		close(start)
		wg.Wait()
	}
}

func TestApp_Get(t *testing.T) {
	want := transcriptionFor("t-1")
	var gotID string
	api := &fakeAPI{
		getFunc: func(ctx context.Context, id string) (*domain.Transcription, error) {
			gotID = id
			return want, nil
		},
	}
	a := &App{api: api}
	a.OnStartup(context.Background())

	got, err := a.Get("t-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotID != "t-1" {
		t.Errorf("Get called with id %q, want %q", gotID, "t-1")
	}
	if got.ID != want.ID {
		t.Errorf("Get returned ID %q, want %q", got.ID, want.ID)
	}
}

func TestApp_Get_PropagatesError(t *testing.T) {
	wantErr := domain.New(domain.ErrCodeNotFound, "no such transcription")
	api := &fakeAPI{
		getFunc: func(ctx context.Context, id string) (*domain.Transcription, error) {
			return nil, wantErr
		},
	}
	a := &App{api: api}

	_, err := a.Get("missing")
	if err != wantErr {
		t.Errorf("Get error = %v, want %v", err, wantErr)
	}
}

func TestApp_List(t *testing.T) {
	api := &fakeAPI{
		listFunc: func(ctx context.Context, f domain.ListFilter) (domain.Page, error) {
			if f.Status != domain.StatusRunning {
				t.Errorf("List called with Status %q, want %q", f.Status, domain.StatusRunning)
			}
			return domain.Page{Items: []*domain.Transcription{transcriptionFor("t-1")}, NextCursor: "next"}, nil
		},
	}
	a := &App{api: api}

	page, err := a.List(ListFilterDTO{Status: string(domain.StatusRunning)})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "t-1" {
		t.Errorf("List returned %+v", page)
	}
	if page.NextCursor != "next" {
		t.Errorf("NextCursor = %q, want %q", page.NextCursor, "next")
	}
}

func TestApp_List_RejectsInvalidStatus(t *testing.T) {
	a := &App{api: &fakeAPI{}}

	_, err := a.List(ListFilterDTO{Status: "not-a-status"})
	if domain.CodeOf(err) != domain.ErrCodeInvalidInput {
		t.Errorf("List error code = %q, want %q", domain.CodeOf(err), domain.ErrCodeInvalidInput)
	}
}

func TestApp_Retry(t *testing.T) {
	api := &fakeAPI{
		retryFunc: func(ctx context.Context, id string) (*domain.Transcription, error) {
			return transcriptionFor(id), nil
		},
	}
	a := &App{api: api}

	got, err := a.Retry("t-1")
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if got.ID != "t-1" {
		t.Errorf("Retry returned ID %q, want %q", got.ID, "t-1")
	}
}

func TestApp_Cancel(t *testing.T) {
	var called bool
	api := &fakeAPI{
		cancelFunc: func(ctx context.Context, id string) (*domain.Transcription, error) {
			called = true
			return transcriptionFor(id), nil
		},
	}
	a := &App{api: api}

	if err := a.Cancel("t-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !called {
		t.Error("Cancel did not call the API")
	}
}

func TestApp_Delete(t *testing.T) {
	var gotID string
	api := &fakeAPI{
		deleteFunc: func(ctx context.Context, id string) error {
			gotID = id
			return nil
		},
	}
	a := &App{api: api}

	if err := a.Delete("t-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gotID != "t-1" {
		t.Errorf("Delete called with id %q, want %q", gotID, "t-1")
	}
}

func TestApp_Health(t *testing.T) {
	api := &fakeAPI{
		healthFunc: func(ctx context.Context) (HealthReport, error) {
			return HealthReport{OK: true, Checks: map[string]CheckResult{"database": {OK: true, Detail: "ok"}}}, nil
		},
	}
	a := &App{api: api}

	got, err := a.Health()
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !got.OK {
		t.Errorf("Health OK = false, want true")
	}
	if !got.Checks["database"].OK {
		t.Errorf("Health Checks[database].OK = false, want true")
	}
}

// TestApp_Submit proves Submit opens FilePath, streams it as the request
// body, and translates the result back into a TranscriptionDTO.
func TestApp_Submit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.wav")
	if err := os.WriteFile(path, []byte("RIFF...fake wav bytes..."), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var gotFilename, gotMediaType string
	var gotBody []byte
	api := &fakeAPI{
		submitFunc: func(ctx context.Context, req httpclient.SubmitRequest) (*domain.Transcription, error) {
			gotFilename = req.Filename
			gotMediaType = req.MediaType
			b := make([]byte, 1024)
			n, _ := req.Body.Read(b)
			gotBody = b[:n]
			return transcriptionFor("t-new"), nil
		},
	}
	a := &App{api: api}

	got, err := a.Submit(SubmitRequestDTO{FilePath: path, Options: OptionsDTO{Model: "base"}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got.ID != "t-new" {
		t.Errorf("Submit returned ID %q, want %q", got.ID, "t-new")
	}
	if gotFilename != "clip.wav" {
		t.Errorf("Submit sent Filename %q, want %q", gotFilename, "clip.wav")
	}
	if gotMediaType != "audio/wav" && gotMediaType != "audio/x-wav" && gotMediaType != "application/octet-stream" {
		t.Errorf("Submit sent unexpected MediaType %q", gotMediaType)
	}
	if string(gotBody) != "RIFF...fake wav bytes..." {
		t.Errorf("Submit streamed body %q, want the file's contents", gotBody)
	}
}

// TestApp_Submit_MissingFile proves a FilePath that cannot be opened fails
// as domain.ErrCodeInvalidInput, never reaching the API at all.
func TestApp_Submit_MissingFile(t *testing.T) {
	a := &App{api: &fakeAPI{}}

	_, err := a.Submit(SubmitRequestDTO{FilePath: "/does/not/exist.wav"})
	if domain.CodeOf(err) != domain.ErrCodeInvalidInput {
		t.Errorf("Submit error code = %q, want %q", domain.CodeOf(err), domain.ErrCodeInvalidInput)
	}
}

// TestApp_ChooseAudioFile proves ChooseAudioFile calls through
// openFileDialog and returns its result unchanged, including the "user
// canceled" ("", nil) case — openFileDialog is a package var (app.go),
// indirecting the real Wails runtime call, for exactly this reason: no
// test in this package can supply the populated ctx runtime.OpenFileDialog
// itself requires.
func TestApp_ChooseAudioFile(t *testing.T) {
	orig := openFileDialog
	defer func() { openFileDialog = orig }()

	var gotTitle string
	openFileDialog = func(ctx context.Context, opts runtime.OpenDialogOptions) (string, error) {
		gotTitle = opts.Title
		return "/home/user/clip.wav", nil
	}

	a := &App{}
	path, err := a.ChooseAudioFile()
	if err != nil {
		t.Fatalf("ChooseAudioFile: %v", err)
	}
	if path != "/home/user/clip.wav" {
		t.Errorf("ChooseAudioFile = %q, want %q", path, "/home/user/clip.wav")
	}
	if gotTitle == "" {
		t.Errorf("ChooseAudioFile did not set a dialog title")
	}
}

// TestApp_ChooseAudioFile_Canceled proves a canceled dialog ("", nil) is
// not turned into an error.
func TestApp_ChooseAudioFile_Canceled(t *testing.T) {
	orig := openFileDialog
	defer func() { openFileDialog = orig }()

	openFileDialog = func(ctx context.Context, opts runtime.OpenDialogOptions) (string, error) {
		return "", nil
	}

	a := &App{}
	path, err := a.ChooseAudioFile()
	if err != nil {
		t.Fatalf("ChooseAudioFile: %v", err)
	}
	if path != "" {
		t.Errorf("ChooseAudioFile = %q, want empty string for a canceled dialog", path)
	}
}

func TestMediaTypeForFile(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "clip.mp3", want: "audio/mpeg"},
		{path: "clip.unknownext", want: "application/octet-stream"},
	}
	for _, tt := range tests {
		if got := mediaTypeForFile(tt.path); got != tt.want {
			t.Errorf("mediaTypeForFile(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
