package whispercpp

import (
	"encoding/json"
	"strings"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// whisperOutput is the subset of whisper-cli's -oj/-ojf JSON this package
// understands (see output_json in examples/cli/cli.cpp upstream). The real
// blob also carries "systeminfo"/"model"/"params" objects describing the
// build and invocation; none of that is decoded here since Transcribe
// already knows everything it needs about its own request.
type whisperOutput struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription []whisperSegment `json:"transcription"`
}

// whisperSegment is one entry of the "transcription" array.
type whisperSegment struct {
	Offsets whisperOffsets `json:"offsets"`
	Text    string         `json:"text"`
	// Tokens is present only when whisper-cli was run with -ojf
	// (Options.WordTimestamps) — see buildArgs.
	Tokens []whisperToken `json:"tokens"`
}

// whisperOffsets is a segment or token's from/to span, in milliseconds —
// upstream's output_json computes these as t0*10/t1*10 from whisper.cpp's
// own centisecond-resolution internal timestamps.
type whisperOffsets struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// whisperToken is one entry of a segment's "tokens" array (-ojf only).
// Offsets is a pointer because upstream only emits it "if we have
// per-token timestamps" (whisper.cpp's dtw/timestamp data can be absent
// for a given token) — the JSON parser must tolerate its absence rather
// than reading a spurious {0,0} span as real.
type whisperToken struct {
	Text    string          `json:"text"`
	Offsets *whisperOffsets `json:"offsets"`
	P       float64         `json:"p"`
}

// parseWhisperJSON is Transcribe's parsing/mapping logic as a pure
// function, so it can be exercised against fixture JSON strings without a
// real whisper-cli binary. opts is the request's Options (specifically
// WordTimestamps, which decides whether tokens are read into
// domain.Segment.Words at all — even if the JSON happens to carry a
// "tokens" array, e.g. from a hand-built fixture, -oj-mode output never
// should).
func parseWhisperJSON(data []byte, opts domain.Options) (*domain.Result, error) {
	var raw whisperOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	segments := make([]domain.Segment, 0, len(raw.Transcription))
	texts := make([]string, 0, len(raw.Transcription))
	var lastEnd float64

	for i, seg := range raw.Transcription {
		start := float64(seg.Offsets.From) / 1000
		end := float64(seg.Offsets.To) / 1000
		text := normalizeSpaces(seg.Text)

		domainSeg := domain.Segment{
			ID:    i,
			Start: start,
			End:   end,
			Text:  text,
		}
		if opts.WordTimestamps {
			domainSeg.Words = mergeWords(seg.Tokens)
		}
		segments = append(segments, domainSeg)

		if text != "" {
			texts = append(texts, text)
		}
		lastEnd = end
	}

	return &domain.Result{
		Text:            normalizeSpaces(strings.Join(texts, " ")),
		Language:        raw.Result.Language,
		DurationSeconds: lastEnd,
		Segments:        segments,
	}, nil
}

// mergeWords maps a segment's tokens into domain.Words: special tokens
// (see isSpecialToken) are dropped entirely, and a sub-word piece — one
// whose text does not start with a space, whisper.cpp's own convention for
// "this token continues the previous word" (byte-pair-encoding routinely
// splits a longer word across several tokens) — is merged into the
// preceding domain.Word rather than starting a new one.
func mergeWords(tokens []whisperToken) []domain.Word {
	var words []domain.Word
	for _, tok := range tokens {
		if isSpecialToken(tok.Text) {
			continue
		}

		var start, end float64
		if tok.Offsets != nil {
			start = float64(tok.Offsets.From) / 1000
			end = float64(tok.Offsets.To) / 1000
		}

		if len(words) > 0 && !strings.HasPrefix(tok.Text, " ") {
			last := &words[len(words)-1]
			last.Word += tok.Text
			if tok.Offsets != nil {
				last.End = end
			}
			continue
		}

		words = append(words, domain.Word{
			Word:        strings.TrimPrefix(tok.Text, " "),
			Start:       start,
			End:         end,
			Probability: tok.P,
		})
	}
	return words
}

// isSpecialToken reports whether text is one of whisper.cpp's own
// non-lexical tokens rather than actual transcribed content. whisper.cpp
// spells these "[_BEG_]", "[_EOT_]", "[_SOT_]", "[_TT_<n>]",
// "[_LANG_<code>]" and similar (see whisper.cpp's src/whisper.cpp vocab
// loader) — the "[_"-prefix check below catches all of them. The
// "<|...|>"-wrapped form is OpenAI Python whisper's own special-token
// spelling, never actually emitted by whisper-cli; it is still checked for
// here defensively (see whispercpp.go's package doc) since a model's vocab
// file could in principle be built with that convention instead, and
// recognising it costs nothing.
func isSpecialToken(text string) bool {
	if strings.HasPrefix(text, "[_") {
		return true
	}
	if strings.HasPrefix(text, "<|") && strings.HasSuffix(text, "|>") {
		return true
	}
	return false
}

// normalizeSpaces collapses any run of whitespace (including whisper.cpp's
// habitual leading space on each segment's text) to a single space and
// trims the result — the contract's "space-normalized and trimmed".
func normalizeSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
