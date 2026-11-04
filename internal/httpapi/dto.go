package httpapi

import (
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// optionsDTO mirrors domain.Options field for field. It exists -- rather
// than marshaling domain.Options directly -- so the wire shape of every
// resource this package serves is declared once, here, independent of
// whatever internal/domain happens to look like.
//
// Language is only a hint for the audio language. Task is accepted for
// compatibility and ignored: the service sets the output language itself
// (see domain.PlanOutput).
type optionsDTO struct {
	Model          string      `json:"model"`
	Language       string      `json:"language"`
	Task           domain.Task `json:"task"`
	WordTimestamps bool        `json:"wordTimestamps"`
	InitialPrompt  string      `json:"initialPrompt"`
	BeamSize       int         `json:"beamSize"`
	Temperature    float64     `json:"temperature"`
}

// optionsFromDomain converts a domain.Options to its wire shape.
func optionsFromDomain(o domain.Options) optionsDTO {
	return optionsDTO{
		Model:          o.Model,
		Language:       o.Language,
		Task:           o.Task,
		WordTimestamps: o.WordTimestamps,
		InitialPrompt:  o.InitialPrompt,
		BeamSize:       o.BeamSize,
		Temperature:    o.Temperature,
	}
}

// toDomain converts an optionsDTO back to domain.Options, e.g. for the
// `options` multipart part decoded straight into this shape by
// submitTranscription.
func (o optionsDTO) toDomain() domain.Options {
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

// audioDTO mirrors domain.Audio field for field.
type audioDTO struct {
	Filename        string  `json:"filename"`
	MediaType       string  `json:"mediaType"`
	SizeBytes       int64   `json:"sizeBytes"`
	DurationSeconds float64 `json:"durationSeconds"`
	SampleRateHz    int     `json:"sampleRateHz"`
	Channels        int     `json:"channels"`
	Codec           string  `json:"codec"`
}

// audioFromDomain converts a domain.Audio to its wire shape.
func audioFromDomain(a domain.Audio) audioDTO {
	return audioDTO{
		Filename:        a.Filename,
		MediaType:       a.MediaType,
		SizeBytes:       a.SizeBytes,
		DurationSeconds: a.DurationSeconds,
		SampleRateHz:    a.SampleRateHz,
		Channels:        a.Channels,
		Codec:           a.Codec,
	}
}

// wordDTO mirrors domain.Word field for field.
type wordDTO struct {
	Word        string  `json:"word"`
	Start       float64 `json:"start"`
	End         float64 `json:"end"`
	Probability float64 `json:"probability"`
}

// segmentDTO mirrors domain.Segment field for field.
type segmentDTO struct {
	ID    int       `json:"id"`
	Start float64   `json:"start"`
	End   float64   `json:"end"`
	Text  string    `json:"text"`
	Words []wordDTO `json:"words,omitempty"`
}

// resultDTO mirrors domain.Result field for field.
type resultDTO struct {
	Text            string       `json:"text"`
	Language        string       `json:"language"`
	OutputLanguage  string       `json:"outputLanguage,omitempty"`
	DurationSeconds float64      `json:"durationSeconds"`
	Model           string       `json:"model"`
	Task            domain.Task  `json:"task"`
	Segments        []segmentDTO `json:"segments"`
}

// resultFromDomain converts a *domain.Result to its wire shape, returning
// nil when r is nil so the transcription DTO's `result` field is omitted
// (via its own omitempty) rather than serialised as an empty object.
func resultFromDomain(r *domain.Result) *resultDTO {
	if r == nil {
		return nil
	}
	segments := make([]segmentDTO, len(r.Segments))
	for i, seg := range r.Segments {
		var words []wordDTO
		if len(seg.Words) > 0 {
			words = make([]wordDTO, len(seg.Words))
			for j, w := range seg.Words {
				words[j] = wordDTO{Word: w.Word, Start: w.Start, End: w.End, Probability: w.Probability}
			}
		}
		segments[i] = segmentDTO{ID: seg.ID, Start: seg.Start, End: seg.End, Text: seg.Text, Words: words}
	}
	return &resultDTO{
		Text:            r.Text,
		Language:        r.Language,
		OutputLanguage:  r.OutputLanguage,
		DurationSeconds: r.DurationSeconds,
		Model:           r.Model,
		Task:            r.Task,
		Segments:        segments,
	}
}

// errorDTO mirrors the shape of both domain.Error (as embedded in a
// transcriptionDTO's `error` field) and the top-level error envelope's
// `error` body -- the two are wire-identical, {code, message, details}, so
// one type serves both call sites.
type errorDTO struct {
	Code    domain.ErrorCode `json:"code"`
	Message string           `json:"message"`
	Details map[string]any   `json:"details,omitempty"`
}

// errorFromDomain converts a *domain.Error to its wire shape, returning
// nil when e is nil so a transcriptionDTO with no error omits the field
// entirely.
func errorFromDomain(e *domain.Error) *errorDTO {
	if e == nil {
		return nil
	}
	return &errorDTO{Code: e.Code, Message: e.Message, Details: e.Details}
}

// transcriptionDTO mirrors CONTRACT.md section 6's transcription resource
// exactly: id, status, options, audio, result (omitempty), error
// (omitempty), attempts, maxAttempts, createdAt, updatedAt,
// startedAt/completedAt (omitempty). It deliberately has no field for
// domain.Transcription's SourcePath, LeaseOwner, LeaseExpiresAt or
// NextAttemptAt -- those are queue/storage implementation details, never
// API surface.
type transcriptionDTO struct {
	ID          string        `json:"id"`
	Status      domain.Status `json:"status"`
	Options     optionsDTO    `json:"options"`
	Audio       audioDTO      `json:"audio"`
	Result      *resultDTO    `json:"result,omitempty"`
	Error       *errorDTO     `json:"error,omitempty"`
	Attempts    int           `json:"attempts"`
	MaxAttempts int           `json:"maxAttempts"`
	CreatedAt   time.Time     `json:"createdAt"`
	UpdatedAt   time.Time     `json:"updatedAt"`
	StartedAt   *time.Time    `json:"startedAt,omitempty"`
	CompletedAt *time.Time    `json:"completedAt,omitempty"`
}

// transcriptionFromDomain converts a *domain.Transcription to its wire
// shape. It is the only place a *domain.Transcription is read for
// serialisation -- handlers must never json.Marshal the domain type
// directly, both to keep an internal field from silently becoming API
// contract, and to get the result/error omitempty handling right in one
// place.
func transcriptionFromDomain(t *domain.Transcription) transcriptionDTO {
	return transcriptionDTO{
		ID:          t.ID,
		Status:      t.Status,
		Options:     optionsFromDomain(t.Options),
		Audio:       audioFromDomain(t.Audio),
		Result:      resultFromDomain(t.Result),
		Error:       errorFromDomain(t.Error),
		Attempts:    t.Attempts,
		MaxAttempts: t.MaxAttempts,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
		StartedAt:   t.StartedAt,
		CompletedAt: t.CompletedAt,
	}
}

// listTranscriptionsResponseDTO mirrors GET /api/v1/transcriptions'
// response body: `{"items":[...],"nextCursor":"..."}`.
type listTranscriptionsResponseDTO struct {
	Items      []transcriptionDTO `json:"items"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

// listTranscriptionsFromPage converts an app.Page to its wire shape.
func listTranscriptionsResponseFromItems(items []*domain.Transcription, nextCursor string) listTranscriptionsResponseDTO {
	dtos := make([]transcriptionDTO, len(items))
	for i, t := range items {
		dtos[i] = transcriptionFromDomain(t)
	}
	return listTranscriptionsResponseDTO{Items: dtos, NextCursor: nextCursor}
}

// errorResponseDTO is the single error envelope every non-2xx JSON
// response in this API uses: `{"error":{"code","message","details"}}`.
type errorResponseDTO struct {
	Error errorDTO `json:"error"`
}

// checkResultDTO mirrors observability.CheckResult.
type checkResultDTO struct {
	OK         bool   `json:"ok"`
	Detail     string `json:"detail,omitempty"`
	FreeBytes  *int64 `json:"freeBytes,omitempty"`
	TotalBytes *int64 `json:"totalBytes,omitempty"`
}

// readyReportDTO mirrors observability.Report, the /readyz response body.
type readyReportDTO struct {
	OK     bool                      `json:"ok"`
	Checks map[string]checkResultDTO `json:"checks"`
}
