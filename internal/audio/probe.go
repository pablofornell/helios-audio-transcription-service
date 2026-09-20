package audio

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// deniedFormats are ffprobe/ffmpeg demuxer names (the "format_name" field,
// see probeFormat) that describe a playlist or otherwise self-declaring
// container rather than an actual media payload. A hostile upload can name
// itself e.g. "evil.m3u8" and point an HLS playlist at an arbitrary local
// path; ffprobe then reports whatever duration the playlist claims (an
// EXTINF tag, never actually decoded) instead of the referenced file's real
// one, which lets HELIOS_AUDIO_MAX_DURATION be bypassed entirely, and
// ffmpeg (see Normalize) would go on to decode that arbitrary local file.
// Denying these demuxers at Probe time closes that off at its root: nothing
// downstream ever gets a path to indirect through in the first place.
// image2/image2pipe are denied for the same reason a playlist is: they are
// ffmpeg's own "read a sequence of files matching a pattern" demuxers, not
// a single media payload.
var deniedFormats = map[string]bool{
	"hls":        true,
	"applehttp":  true,
	"concat":     true,
	"sbg":        true,
	"image2":     true,
	"image2pipe": true,
	"dash":       true,
}

// checkFormatDenied rejects a probed format_name that names a denied
// demuxer. ffprobe's format_name is a comma-separated list when several
// demuxers could plausibly have produced the same bytes (e.g.
// "mov,mp4,m4a,3gp,3g2,mj2"), so every element is checked individually —
// matching the whole string against the deny list would silently let a
// denied format through disguised behind an allowed one earlier in the
// list, or vice versa reject an allowed format that merely shares a list
// with an unrelated denied name.
func checkFormatDenied(formatName string) error {
	for _, part := range strings.Split(formatName, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if deniedFormats[part] {
			return domain.New(domain.ErrCodeUnsupportedMedia,
				"audio: playlist or aggregating container formats are not accepted").
				WithDetails("format", formatName)
		}
	}
	return nil
}

// probeStream is the subset of one ffprobe stream object this package
// understands. sample_rate/duration are strings in ffprobe's own JSON
// output (verified against a real ffprobe run), not numbers, so they are
// decoded as strings and parsed by hand.
type probeStream struct {
	CodecType  string `json:"codec_type"`
	CodecName  string `json:"codec_name"`
	SampleRate string `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Duration   string `json:"duration"`
}

// probeFormat is the subset of ffprobe's "format" object this package
// understands. Duration lives here when the container carries it at the
// format level rather than (or in addition to) the stream level — see
// resolveDuration. FormatName is the demuxer(s) ffprobe used, checked
// against deniedFormats before anything else in this file trusts the rest
// of the report.
type probeFormat struct {
	Duration   string `json:"duration"`
	FormatName string `json:"format_name"`
}

// probeOutput is `ffprobe -print_format json -show_format -show_streams`'s
// output, as far as this package cares: the "formats" list carries many
// more fields (tags, disposition flags, bit rate) that Probe has no use
// for and so never decodes.
type probeOutput struct {
	Format  probeFormat   `json:"format"`
	Streams []probeStream `json:"streams"`
}

// probeArgs are ffprobe's flags for a JSON format+stream report, exactly as
// the contract specifies, plus -protocol_whitelist file: ffprobe's demuxers
// can themselves open further URLs (an HLS/DASH playlist's segment list, a
// concat script's member files, ...), and without an explicit whitelist
// ffmpeg/ffprobe fall back to allowing nearly every protocol they were
// built with. Restricting it to "file" means a container can never make
// this process reach out over http(s) or any other protocol handler no
// matter what it claims to reference — defense in depth alongside the
// format_name deny list below, which is what actually stops the
// same-protocol (local-file-to-local-file) attack this pairs with.
func probeArgs(absPath string) []string {
	return []string{
		"-v", "error",
		"-protocol_whitelist", "file",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		absPath,
	}
}

// Probe implements app.AudioProcessor.Probe (see internal/app's ports.go):
// it shells out to ffprobe, parses its JSON report, and fills
// Duration/SampleRate/Channels/Codec from the first audio stream.
// Filename, MediaType and SizeBytes are left zero — those are known at
// upload time and are the caller's job to preserve.
func (p *Processor) Probe(ctx context.Context, absPath string) (domain.Audio, error) {
	out, truncated, res := p.runCaptureStdout(ctx, p.cfg.FFprobeBin, probeArgs(absPath))
	if truncated {
		// A real ffprobe -show_format/-show_streams report is a few KiB
		// even for a many-stream file (see maxProbeJSONBytes' doc comment);
		// one that exceeds the cap is not a report this package will ever
		// successfully parse, and is itself suspicious enough to reject
		// outright rather than hand a truncated blob to parseProbeJSON.
		return domain.Audio{}, domain.New(domain.ErrCodeUnsupportedMedia,
			"audio: ffprobe output exceeded the size limit").
			WithDetails("maxBytes", maxProbeJSONBytes)
	}
	if res.Err != nil || res.ExitCode != 0 {
		code := classify(res.Stderr, res.ExitCode, res.Err)
		return domain.Audio{}, withStderrTail(domain.Wrap(code, "audio: ffprobe failed", res.Err), res)
	}
	return parseProbeJSON(out, p.cfg.MaxDuration)
}

// parseProbeJSON is Probe's parsing/mapping logic as a pure function, so it
// can be exercised against fixture JSON strings without a real ffprobe
// binary. maxDuration<=0 disables the duration ceiling entirely.
func parseProbeJSON(data []byte, maxDuration time.Duration) (domain.Audio, error) {
	var raw probeOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return domain.Audio{}, domain.Wrap(domain.ErrCodeToolFailure, "audio: could not parse ffprobe JSON", err)
	}

	if err := checkFormatDenied(raw.Format.FormatName); err != nil {
		return domain.Audio{}, err
	}

	stream, ok := firstAudioStream(raw.Streams)
	if !ok {
		return domain.Audio{}, domain.New(domain.ErrCodeNoAudioStream, "audio: file has no audio stream")
	}

	audio := domain.Audio{
		DurationSeconds: resolveDuration(raw.Format, stream),
		SampleRateHz:    int(parseIntOrZero(stream.SampleRate)),
		Channels:        stream.Channels,
		Codec:           stream.CodecName,
	}

	if maxDuration > 0 && audio.DurationSeconds > maxDuration.Seconds() {
		return domain.Audio{}, domain.New(domain.ErrCodePayloadTooLarge, "audio: duration exceeds the configured maximum").
			WithDetails("durationSeconds", audio.DurationSeconds).
			WithDetails("maxDurationSeconds", maxDuration.Seconds())
	}

	return audio, nil
}

// firstAudioStream returns the first stream whose codec_type is "audio",
// mirroring the contract's "fills ... from the FIRST audio stream" —
// ffprobe lists streams in container order, and a file can carry a video
// stream (or more than one audio stream, e.g. a dub track) ahead of the
// one this package cares about.
func firstAudioStream(streams []probeStream) (probeStream, bool) {
	for _, s := range streams {
		if s.CodecType == "audio" {
			return s, true
		}
	}
	return probeStream{}, false
}

// resolveDuration picks a duration in seconds from whichever of the three
// places ffprobe put it: the stream object, the format object, or neither
// (0, "duration absent" — not itself an error; MaxDuration simply cannot
// reject what it cannot see). The stream's own duration is preferred over
// the format's because it describes the audio track itself, not the
// container as a whole (which, for a multi-stream file, can differ from
// any one stream's length).
func resolveDuration(format probeFormat, stream probeStream) float64 {
	if d, ok := parseFloatOK(stream.Duration); ok {
		return d
	}
	if d, ok := parseFloatOK(format.Duration); ok {
		return d
	}
	return 0
}

// parseFloatOK parses s as a float64, reporting ok=false for an empty or
// unparsable value rather than silently returning 0 — resolveDuration
// needs to tell "absent" apart from "present and legitimately zero".
func parseFloatOK(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// parseIntOrZero parses s (e.g. ffprobe's sample_rate, "44100") as an
// int64, treating an empty or unparsable value as zero (unknown) rather
// than an error — a stream ffprobe could not determine a sample rate for
// must not fail the whole probe over one missing field.
func parseIntOrZero(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
