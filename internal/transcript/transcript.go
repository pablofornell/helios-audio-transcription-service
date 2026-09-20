// Package transcript renders a finished domain.Result as the file formats
// clients can request from the transcript endpoint: plain text, SubRip
// (.srt) and WebVTT (.vtt), alongside the JSON representation of the
// Result itself. It has no state and no dependencies beyond domain, so the
// HTTP handler can call it directly with whatever *domain.Result it loaded.
package transcript

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// Format is one of the transcript representations the API can serve for a
// succeeded job, selected by the `?format=` query parameter.
type Format string

const (
	// FormatJSON returns the domain.Result verbatim as JSON. It is the
	// default when the caller does not specify a format.
	FormatJSON Format = "json"
	// FormatText returns the plain-text transcript with no timing.
	FormatText Format = "txt"
	// FormatSRT returns a SubRip (.srt) subtitle file.
	FormatSRT Format = "srt"
	// FormatVTT returns a WebVTT (.vtt) subtitle file.
	FormatVTT Format = "vtt"
)

// ParseFormat validates a caller-supplied format string. An empty string
// defaults to FormatJSON, matching the HTTP route's documented default; any
// other unrecognised value is an *domain.Error{Code: ErrCodeInvalidInput}
// naming the offending field so the HTTP layer can render it directly into
// the error envelope.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case "":
		return FormatJSON, nil
	case FormatJSON, FormatText, FormatSRT, FormatVTT:
		return Format(s), nil
	default:
		return "", domain.New(domain.ErrCodeInvalidInput, fmt.Sprintf("unknown transcript format %q", s)).
			WithDetails("field", "format")
	}
}

// ContentType is the MIME type the transcript endpoint sets for f, per the
// contract's fixed Content-Type table. Any Format not explicitly listed
// (there is none besides the four constants above) falls back to the JSON
// type rather than an empty string.
func (f Format) ContentType() string {
	switch f {
	case FormatText:
		return "text/plain; charset=utf-8"
	case FormatSRT:
		return "application/x-subrip; charset=utf-8"
	case FormatVTT:
		return "text/vtt; charset=utf-8"
	default:
		return "application/json"
	}
}

// Render produces the response body for f. FormatJSON marshals r itself
// (which for a nil r is the JSON literal "null", matching encoding/json's
// normal behaviour); the other formats delegate to the Render* functions
// below, which handle a nil r by returning empty output instead of
// panicking.
func Render(r *domain.Result, f Format) ([]byte, error) {
	switch f {
	case FormatText:
		return []byte(RenderText(r)), nil
	case FormatSRT:
		return []byte(RenderSRT(r)), nil
	case FormatVTT:
		return []byte(RenderVTT(r)), nil
	case FormatJSON:
		return json.Marshal(r)
	default:
		return nil, domain.New(domain.ErrCodeInvalidInput, fmt.Sprintf("unknown transcript format %q", f)).
			WithDetails("field", "format")
	}
}

// RenderText joins every segment's trimmed text with a single space. Whisper
// emits a leading space on each segment, so the join must trim first or the
// output would be riddled with doubled whitespace at every boundary; a
// segment that trims to nothing (e.g. a pure-silence span) is dropped
// entirely rather than leaving a stray extra space. When r has no segments
// at all it falls back to r.Text, since a result can be constructed (e.g. by
// a fake transcriber, or a caller who only needs the summary) without a
// segment breakdown. A nil r returns "".
func RenderText(r *domain.Result) string {
	if r == nil {
		return ""
	}
	if len(r.Segments) == 0 {
		return r.Text
	}
	parts := make([]string, 0, len(r.Segments))
	for _, seg := range r.Segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, " ")
}

// RenderSRT renders r as a SubRip (.srt) file: one CRLF-terminated block per
// non-empty segment, `N\r\nHH:MM:SS,mmm --> HH:MM:SS,mmm\r\ntext\r\n\r\n`,
// 1-based and consecutively indexed (a skipped blank segment does not leave
// a gap in the numbering). CRLF is the SRT convention, not merely Go's
// os-default line ending, so it is hard-coded rather than derived from the
// platform. A nil r, or one with no renderable segments, returns "".
func RenderSRT(r *domain.Result) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	index := 1
	for _, seg := range r.Segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "%d\r\n", index)
		fmt.Fprintf(&b, "%s --> %s\r\n", formatTimestamp(seg.Start, ','), formatTimestamp(seg.End, ','))
		b.WriteString(strings.ReplaceAll(text, "\n", "\r\n"))
		b.WriteString("\r\n\r\n")
		index++
	}
	return b.String()
}

// RenderVTT renders r as a WebVTT (.vtt) file: the mandatory "WEBVTT\n\n"
// header followed by one LF-terminated cue per non-empty segment,
// `HH:MM:SS.mmm --> HH:MM:SS.mmm\ntext\n\n`. Unlike RenderSRT, the header is
// still emitted for a non-nil r with zero cues, since "WEBVTT" alone is
// itself a valid (empty) WebVTT file; only a nil r returns "" outright.
func RenderVTT(r *domain.Result) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, seg := range r.Segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "%s --> %s\n", formatTimestamp(seg.Start, '.'), formatTimestamp(seg.End, '.'))
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	return b.String()
}

// formatTimestamp renders seconds as HH:MM:SS<sep>mmm. Hours are not capped
// at two digits' worth (24) — %02d only sets a minimum width — so timings
// past one hour render correctly with no extra handling. A negative input
// (which should not occur, but a clock skew or a hand-built Result could
// produce one) is clamped to zero rather than emitting a negative or
// wrapping-around timestamp.
func formatTimestamp(seconds float64, sep rune) string {
	if seconds < 0 {
		seconds = 0
	}
	// Multiplying a float64 seconds value by 1000 can leave it a hair under
	// the intended millisecond integer (e.g. 2.4 -> 2399.999999999997), which
	// would truncate one millisecond short. A microsecond-scale epsilon
	// corrects that representation noise without rounding up any timestamp
	// that is genuinely mid-millisecond: whisper.cpp's own timestamps never
	// carry sub-millisecond precision.
	totalMs := int64(math.Floor(seconds*1000 + 1e-6))

	ms := totalMs % 1000
	totalSeconds := totalMs / 1000
	s := totalSeconds % 60
	totalMinutes := totalSeconds / 60
	m := totalMinutes % 60
	h := totalMinutes / 60

	return fmt.Sprintf("%02d:%02d:%02d%c%03d", h, m, s, sep, ms)
}
