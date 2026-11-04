package main

import (
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// This file defines the wire shapes Wails' TypeScript generator turns into
// the frontend's models. They mirror internal/httpapi/dto.go field for
// field (same JSON tags) so the two front doors -- this binding and the
// REST API -- describe a transcription identically: a UI developer reading
// docs/openapi.yaml should never have to re-learn a shape here. They are
// the desktop app's own types, never domain types directly, purely so a
// future internal-only field added to domain.Transcription does not
// silently become frontend API surface just because it round-tripped
// through Wails' AST scanner.

// OptionsDTO mirrors httpapi's optionsDTO / domain.Options field for field.
// Language is only a hint for the audio language. Task is accepted for
// compatibility and ignored: the service sets the output language itself
// (see domain.PlanOutput).
type OptionsDTO struct {
	Model          string      `json:"model"`
	Language       string      `json:"language"`
	Task           domain.Task `json:"task"`
	WordTimestamps bool        `json:"wordTimestamps"`
	InitialPrompt  string      `json:"initialPrompt"`
	BeamSize       int         `json:"beamSize"`
	Temperature    float64     `json:"temperature"`
}

// toDomain converts an OptionsDTO to domain.Options. The zero value (no
// "options" sent at all) yields domain.Options{}, which
// domain.Options.Normalize/app.Service.Submit already treat as "use every
// default" -- see domain.Options.Normalize's own doc.
func (o OptionsDTO) toDomain() domain.Options {
	return domain.Options{
		Model:          o.Model,
		Language:       o.Language,
		Task:           o.Task,
		WordTimestamps: o.WordTimestamps,
		InitialPrompt:  o.InitialPrompt,
		BeamSize:       o.BeamSize,
		Temperature:    o.Temperature,
	}
}

func optionsFromDomain(o domain.Options) OptionsDTO {
	return OptionsDTO{
		Model:          o.Model,
		Language:       o.Language,
		Task:           o.Task,
		WordTimestamps: o.WordTimestamps,
		InitialPrompt:  o.InitialPrompt,
		BeamSize:       o.BeamSize,
		Temperature:    o.Temperature,
	}
}

// AudioDTO mirrors httpapi's audioDTO / domain.Audio field for field.
type AudioDTO struct {
	Filename        string  `json:"filename"`
	MediaType       string  `json:"mediaType"`
	SizeBytes       int64   `json:"sizeBytes"`
	DurationSeconds float64 `json:"durationSeconds"`
	SampleRateHz    int     `json:"sampleRateHz"`
	Channels        int     `json:"channels"`
	Codec           string  `json:"codec"`
}

func audioFromDomain(a domain.Audio) AudioDTO {
	return AudioDTO{
		Filename:        a.Filename,
		MediaType:       a.MediaType,
		SizeBytes:       a.SizeBytes,
		DurationSeconds: a.DurationSeconds,
		SampleRateHz:    a.SampleRateHz,
		Channels:        a.Channels,
		Codec:           a.Codec,
	}
}

// WordDTO mirrors httpapi's wordDTO / domain.Word field for field.
type WordDTO struct {
	Word        string  `json:"word"`
	Start       float64 `json:"start"`
	End         float64 `json:"end"`
	Probability float64 `json:"probability"`
}

// SegmentDTO mirrors httpapi's segmentDTO / domain.Segment field for field.
type SegmentDTO struct {
	ID    int       `json:"id"`
	Start float64   `json:"start"`
	End   float64   `json:"end"`
	Text  string    `json:"text"`
	Words []WordDTO `json:"words,omitempty"`
}

// ResultDTO mirrors httpapi's resultDTO / domain.Result field for field.
type ResultDTO struct {
	Text            string       `json:"text"`
	Language        string       `json:"language"`
	OutputLanguage  string       `json:"outputLanguage,omitempty"`
	DurationSeconds float64      `json:"durationSeconds"`
	Model           string       `json:"model"`
	Task            domain.Task  `json:"task"`
	Segments        []SegmentDTO `json:"segments"`
}

// resultFromDomain converts a *domain.Result to its wire shape, returning
// nil when r is nil so a transcriptionDTO with no result yet omits the
// field entirely (via its own omitempty), matching httpapi's own
// resultFromDomain.
func resultFromDomain(r *domain.Result) *ResultDTO {
	if r == nil {
		return nil
	}
	segments := make([]SegmentDTO, len(r.Segments))
	for i, seg := range r.Segments {
		var words []WordDTO
		if len(seg.Words) > 0 {
			words = make([]WordDTO, len(seg.Words))
			for j, w := range seg.Words {
				words[j] = WordDTO{Word: w.Word, Start: w.Start, End: w.End, Probability: w.Probability}
			}
		}
		segments[i] = SegmentDTO{ID: seg.ID, Start: seg.Start, End: seg.End, Text: seg.Text, Words: words}
	}
	return &ResultDTO{
		Text:            r.Text,
		Language:        r.Language,
		OutputLanguage:  r.OutputLanguage,
		DurationSeconds: r.DurationSeconds,
		Model:           r.Model,
		Task:            r.Task,
		Segments:        segments,
	}
}

// errorDTOFromDomain converts a *domain.Error to its wire shape, returning
// nil when e is nil so a transcriptionDTO with no error omits the field
// entirely -- the same ErrorDTO errors.go's formatError uses for a failed
// bound call itself, so a TranscriptionDTO.Error and a rejected promise's
// error read identically to the frontend.
func errorDTOFromDomain(e *domain.Error) *ErrorDTO {
	if e == nil {
		return nil
	}
	return &ErrorDTO{Code: e.Code, Message: e.Message, Details: e.Details}
}

// TranscriptionDTO mirrors httpapi's transcriptionDTO field for field. It
// is the wire shape for every bound method that returns one transcription
// (Submit, Get, Retry, and each item of a List page).
type TranscriptionDTO struct {
	ID          string        `json:"id"`
	Status      domain.Status `json:"status"`
	Options     OptionsDTO    `json:"options"`
	Audio       AudioDTO      `json:"audio"`
	Result      *ResultDTO    `json:"result,omitempty"`
	Error       *ErrorDTO     `json:"error,omitempty"`
	Attempts    int           `json:"attempts"`
	MaxAttempts int           `json:"maxAttempts"`
	CreatedAt   time.Time     `json:"createdAt"`
	UpdatedAt   time.Time     `json:"updatedAt"`
	StartedAt   *time.Time    `json:"startedAt,omitempty"`
	CompletedAt *time.Time    `json:"completedAt,omitempty"`
	PriorityAt  *time.Time    `json:"priorityAt,omitempty"`
}

// transcriptionFromDomain converts a *domain.Transcription to its wire
// shape. Like httpapi's own transcriptionFromDomain, this is the only
// place a *domain.Transcription is read for serialisation to the
// frontend -- it deliberately has no field for SourcePath, LeaseOwner,
// LeaseToken, LeaseExpiresAt or NextAttemptAt, all queue/storage
// implementation detail, never API surface (see domain.Transcription's own
// doc).
func transcriptionFromDomain(t *domain.Transcription) TranscriptionDTO {
	return TranscriptionDTO{
		ID:          t.ID,
		Status:      t.Status,
		Options:     optionsFromDomain(t.Options),
		Audio:       audioFromDomain(t.Audio),
		Result:      resultFromDomain(t.Result),
		Error:       errorDTOFromDomain(t.Error),
		Attempts:    t.Attempts,
		MaxAttempts: t.MaxAttempts,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
		StartedAt:   t.StartedAt,
		CompletedAt: t.CompletedAt,
		PriorityAt:  t.PriorityAt,
	}
}

// SubmitRequestDTO is the argument to the Submit binding: a local file
// path chosen via ChooseAudioFile (or dropped onto the composer, whose
// absolute path the frontend already has from Wails' native drag-and-drop
// support), plus the transcription options to submit it with. Unlike the
// sibling ingestion service's SubmitRequestDTO, there is no URL field --
// this service transcribes an uploaded file, not a fetched one.
type SubmitRequestDTO struct {
	FilePath string     `json:"filePath"`
	Options  OptionsDTO `json:"options"`
}

// ListFilterDTO mirrors the query parameters GET /api/v1/transcriptions
// takes (see docs/openapi.yaml) -- the argument to the List binding.
type ListFilterDTO struct {
	Library bool   `json:"library,omitempty"`
	Status  string `json:"status,omitempty"`
	Cursor  string `json:"cursor,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// toDomain converts f to a domain.ListFilter, rejecting a Status that is
// not one of the five defined domain.Status values -- the same check
// internal/httpapi's listTranscriptions applies to its own `status` query
// parameter, so a malformed filter fails identically through either front
// door rather than silently matching nothing server-side.
func (f ListFilterDTO) toDomain() (domain.ListFilter, error) {
	status := domain.Status(f.Status)
	if status != "" && !status.Valid() {
		return domain.ListFilter{}, domain.New(domain.ErrCodeInvalidInput, "invalid status "+f.Status).
			WithDetails("field", "status")
	}
	return domain.ListFilter{Status: status, Cursor: f.Cursor, Limit: f.Limit, Library: f.Library}, nil
}

// ListPageDTO mirrors httpapi's listTranscriptionsResponseDTO.
type ListPageDTO struct {
	Items      []TranscriptionDTO `json:"items"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

func listPageFromDomain(p domain.Page) ListPageDTO {
	items := make([]TranscriptionDTO, len(p.Items))
	for i, t := range p.Items {
		items[i] = transcriptionFromDomain(t)
	}
	return ListPageDTO{Items: items, NextCursor: p.NextCursor}
}

// MediaLocationDTO is the result of the AudioLocation binding (reveal.go):
// whether this process already holds a locally cached copy of id's audio
// file, and if so, its real, absolute path -- see reveal.go's own package
// doc for why "locally cached", not "locally accessible on the server's
// own disk", is what this means in a remote-only client.
type MediaLocationDTO struct {
	Path   string `json:"path,omitempty"`
	Exists bool   `json:"exists"`
}

// CheckResultDTO mirrors httpapi's checkResultDTO / observability.CheckResult.
type CheckResultDTO struct {
	OK         bool   `json:"ok"`
	Detail     string `json:"detail"`
	FreeBytes  *int64 `json:"freeBytes,omitempty"`
	TotalBytes *int64 `json:"totalBytes,omitempty"`
}

// HealthReportDTO mirrors httpapi's readyReportDTO / observability.Report.
type HealthReportDTO struct {
	OK     bool                      `json:"ok"`
	Checks map[string]CheckResultDTO `json:"checks"`
}

func healthReportDTOFromReport(r HealthReport) HealthReportDTO {
	checks := make(map[string]CheckResultDTO, len(r.Checks))
	for name, c := range r.Checks {
		checks[name] = CheckResultDTO{OK: c.OK, Detail: c.Detail, FreeBytes: c.FreeBytes, TotalBytes: c.TotalBytes}
	}
	return HealthReportDTO{OK: r.OK, Checks: checks}
}
