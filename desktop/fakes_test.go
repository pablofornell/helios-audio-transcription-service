package main

import (
	"context"
	"io"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/httpclient"
	"github.com/pablofornell/helios-audio-transcription-service/internal/transcript"
)

// fakeAPI is a hand-written transcriptionAPI double, in the same style as
// the sibling ingestion service's own fakes_test.go: no mocking library,
// one configurable func field per method, and a panic — not a silent zero
// value — when a test reaches a method it never configured, so an
// unexpectedly-exercised call says so loudly instead of masking a bug.
type fakeAPI struct {
	freeStorageBytesFunc func(ctx context.Context) (uint64, error)
	submitFunc           func(ctx context.Context, req httpclient.SubmitRequest) (*domain.Transcription, error)
	getFunc              func(ctx context.Context, id string) (*domain.Transcription, error)
	listFunc             func(ctx context.Context, f domain.ListFilter) (domain.Page, error)
	deleteFunc           func(ctx context.Context, id string) error
	cancelFunc           func(ctx context.Context, id string) (*domain.Transcription, error)
	retryFunc            func(ctx context.Context, id string) (*domain.Transcription, error)
	prioritizeFunc       func(ctx context.Context, id string) (*domain.Transcription, error)
	getTranscriptFunc    func(ctx context.Context, id string, format transcript.Format) ([]byte, error)
	openAudioFunc        func(ctx context.Context, id string) (io.ReadSeekCloser, httpclient.AudioInfo, error)
	healthFunc           func(ctx context.Context) (HealthReport, error)
}

var _ transcriptionAPI = (*fakeAPI)(nil)

func (f *fakeAPI) FreeStorageBytes(ctx context.Context) (uint64, error) {
	if f.freeStorageBytesFunc == nil {
		panic("fakeAPI: FreeStorageBytes not configured")
	}
	return f.freeStorageBytesFunc(ctx)
}

func (f *fakeAPI) Submit(ctx context.Context, req httpclient.SubmitRequest) (*domain.Transcription, error) {
	if f.submitFunc == nil {
		panic("fakeAPI: Submit not configured")
	}
	return f.submitFunc(ctx, req)
}

func (f *fakeAPI) Get(ctx context.Context, id string) (*domain.Transcription, error) {
	if f.getFunc == nil {
		panic("fakeAPI: Get not configured")
	}
	return f.getFunc(ctx, id)
}

func (f *fakeAPI) List(ctx context.Context, filter domain.ListFilter) (domain.Page, error) {
	if f.listFunc == nil {
		panic("fakeAPI: List not configured")
	}
	return f.listFunc(ctx, filter)
}

func (f *fakeAPI) Delete(ctx context.Context, id string) error {
	if f.deleteFunc == nil {
		panic("fakeAPI: Delete not configured")
	}
	return f.deleteFunc(ctx, id)
}

func (f *fakeAPI) Cancel(ctx context.Context, id string) (*domain.Transcription, error) {
	if f.cancelFunc == nil {
		panic("fakeAPI: Cancel not configured")
	}
	return f.cancelFunc(ctx, id)
}

func (f *fakeAPI) Retry(ctx context.Context, id string) (*domain.Transcription, error) {
	if f.retryFunc == nil {
		panic("fakeAPI: Retry not configured")
	}
	return f.retryFunc(ctx, id)
}

func (f *fakeAPI) Prioritize(ctx context.Context, id string) (*domain.Transcription, error) {
	if f.prioritizeFunc == nil {
		panic("fakeAPI: Prioritize not configured")
	}
	return f.prioritizeFunc(ctx, id)
}

func (f *fakeAPI) GetTranscript(ctx context.Context, id string, format transcript.Format) ([]byte, error) {
	if f.getTranscriptFunc == nil {
		panic("fakeAPI: GetTranscript not configured")
	}
	return f.getTranscriptFunc(ctx, id, format)
}

func (f *fakeAPI) OpenAudio(ctx context.Context, id string) (io.ReadSeekCloser, httpclient.AudioInfo, error) {
	if f.openAudioFunc == nil {
		panic("fakeAPI: OpenAudio not configured")
	}
	return f.openAudioFunc(ctx, id)
}

func (f *fakeAPI) Health(ctx context.Context) (HealthReport, error) {
	if f.healthFunc == nil {
		panic("fakeAPI: Health not configured")
	}
	return f.healthFunc(ctx)
}

// nopCloseReadSeeker adapts a *bytes.Reader (or any io.ReadSeeker) to
// io.ReadSeekCloser for fakeAPI.openAudioFunc — a real
// httpclient.Client.OpenAudio's reader always has something to release;
// a test's canned bytes.Reader does not.
type nopCloseReadSeeker struct {
	io.ReadSeeker
}

func (nopCloseReadSeeker) Close() error { return nil }
