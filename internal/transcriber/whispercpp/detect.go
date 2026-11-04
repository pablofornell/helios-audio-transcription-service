package whispercpp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// detectedLanguageRE matches the log line whisper.cpp prints when it
// detects the language. Real output of whisper-cli v1.9.4 with -dl:
//
//	whisper_full_with_state: auto-detected language: en (p = 0.976673)
//
// See testdata/detect_language_jfk_tiny.stderr. Group 1 is the code. Group 2
// is the probability, which is optional here: a missing one counts as 0.
var detectedLanguageRE = regexp.MustCompile(`auto-detected language:\s+(\S+)(?:\s+\(p = ([0-9.eE+-]+)\))?`)

// languageCodeRE is the shape of a code Transcribe accepts from detection:
// 2 or 3 lowercase letters, like domain.Options.Language.
var languageCodeRE = regexp.MustCompile(`^[a-z]{2,3}$`)

// maxScanLine bounds the part of one stderr line the scanner keeps.
const maxScanLine = 4 * 1024

// Detection tuning. whisper.cpp reads only the first 30 s window of its
// input for language detection, and the model is unsure when that window
// holds no speech: real runs of 35 s of silence before French audio gave
// "en" with p = 0.354, while normal speech gave p = 0.969 to 0.999.
const (
	// detectWindow is the length of the clip cut for one detection run.
	detectWindow = 30 * time.Second
	// lowConfidence is the probability under which the service looks at
	// later windows of the audio.
	lowConfidence = 0.5
	// silenceFilter removes the silence before the first sound. ffmpeg
	// keeps 0.3 s of it.
	silenceFilter = "silenceremove=start_periods=1:start_threshold=-45dB:start_duration=0.1:start_silence=0.3"
	// pcmBytesPerSecond and wavHeaderBytes describe the WAV the audio step
	// produces (16 kHz, mono, 16 bit). They give the duration of the audio
	// from its file size.
	pcmBytesPerSecond = 16000 * 2
	wavHeaderBytes    = 44
	// minClipBytes is the smallest clip (0.5 s) worth a detection run.
	// ffmpeg writes a header-only file when the audio is all silence.
	minClipBytes = pcmBytesPerSecond / 2
	// minWindowedAudio is the shortest audio that gets extra windows. A
	// shorter file has no later part that differs from the first.
	minWindowedAudio = 6 * time.Second
)

// languageScanner is an io.Writer that finds the "auto-detected language"
// line in a stream of stderr bytes. The 2 KiB stderr tail in runResult is
// too small for this job: whisper.cpp prints the line in the middle of its
// log, and timing lines follow it.
type languageScanner struct {
	line  []byte
	found bool
	code  string
	prob  float64
}

func (s *languageScanner) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			s.endLine()
			continue
		}
		if len(s.line) < maxScanLine {
			s.line = append(s.line, b)
		}
	}
	return len(p), nil
}

// endLine checks the finished line. The first match wins.
func (s *languageScanner) endLine() {
	if !s.found {
		if m := detectedLanguageRE.FindSubmatch(s.line); m != nil {
			s.found = true
			s.code = string(m[1])
			if len(m[2]) > 0 {
				s.prob, _ = strconv.ParseFloat(string(m[2]), 64)
			}
		}
	}
	s.line = s.line[:0]
}

// result returns the detected code and probability. The code is "" when
// stderr had no such line.
func (s *languageScanner) result() (string, float64) {
	s.endLine() // the last line may have no newline
	return s.code, s.prob
}

// detection is the outcome of the language detection for one job.
type detection struct {
	// Language is the detected code.
	Language string
	// Probability is the confidence of whisper.cpp for Language, 0 to 1.
	Probability float64
	// Window names the part of the audio that gave this result.
	Window string
}

// detectLanguage finds the language spoken in req.AudioPath. It never takes
// the language from the request.
//
// whisper.cpp looks only at the first 30 s of its input, and a window with
// no speech gives a weak, wrong answer (an "en" with p = 0.35 for French
// after 35 s of silence). So the service:
//
//  1. cuts a clip from the start of the audio with the leading silence
//     removed (ffmpeg silenceremove), and detects on that clip;
//  2. if the probability is under lowConfidence, also detects on clips cut
//     at one third and two thirds of the audio, and keeps the result with
//     the highest probability.
//
// A low probability alone never fails the job: audio with no speech is
// valid, and the best result stays. All runs use the same WAV, the same
// process limits and the caller's ctx, so a cancel or a timeout kills the
// running process group. The clips live in a temp dir next to the audio,
// removed before this function returns.
//
// A failed whisper-cli run gets the same error code as a failed
// transcription run (classifyRun). A zero exit with no usable code is a
// tool_failure, which is retryable like any other tool failure. A failed
// ffmpeg clip is not an error: the service goes on with the audio as it is.
func (t *Transcriber) detectLanguage(ctx context.Context, req domain.TranscribeRequest, modelPath string) (detection, error) {
	dir, err := os.MkdirTemp(filepath.Dir(req.AudioPath), ".whisper-detect-")
	if err != nil {
		return detection{}, domain.Wrap(domain.ErrCodeInternal, "whispercpp: create detection temp dir", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// Window 0: the start of the audio, without its leading silence. When
	// no clip is possible (ffmpeg failed, or the audio is all silence) the
	// run uses the audio as it is.
	clip, err := t.cutClip(ctx, req.AudioPath, dir, "start", 0)
	if err != nil {
		return detection{}, err
	}
	if clip == "" {
		clip = req.AudioPath
	}
	best, err := t.detectClip(ctx, clip, modelPath, "start")
	if err != nil {
		return detection{}, err
	}

	if best.Probability < lowConfidence {
		for _, w := range laterWindows(req.AudioPath) {
			clip, err := t.cutClip(ctx, req.AudioPath, dir, w.name, w.offset)
			if err != nil {
				return detection{}, err
			}
			if clip == "" {
				continue
			}
			d, err := t.detectClip(ctx, clip, modelPath, w.name)
			if err != nil {
				return detection{}, err
			}
			if d.Probability > best.Probability {
				best = d
			}
		}
	}

	t.cfg.Logger.InfoContext(ctx, "whispercpp: language detected",
		"language", best.Language, "probability", best.Probability, "window", best.Window,
		"low_confidence", best.Probability < lowConfidence)
	return best, nil
}

// window is a part of the audio a detection run can look at.
type window struct {
	name   string
	offset time.Duration
}

// laterWindows returns the windows at one third and two thirds of the audio.
// It returns none for audio shorter than minWindowedAudio, or when the
// audio file cannot be measured.
func laterWindows(audioPath string) []window {
	info, err := os.Stat(audioPath)
	if err != nil || info.Size() <= wavHeaderBytes {
		return nil
	}
	total := time.Duration(float64(info.Size()-wavHeaderBytes) / pcmBytesPerSecond * float64(time.Second))
	if total < minWindowedAudio {
		return nil
	}
	return []window{
		{name: "one-third", offset: total / 3},
		{name: "two-thirds", offset: total * 2 / 3},
	}
}

// cutClip runs ffmpeg to cut a clip of up to detectWindow from audio,
// starting at offset, with the leading silence removed. It returns the clip
// path, or "" when there is no usable clip. It returns an error only for a
// cancel or a timeout; any other ffmpeg failure is logged and gives "".
func (t *Transcriber) cutClip(ctx context.Context, audio, dir, name string, offset time.Duration) (string, error) {
	out := filepath.Join(dir, name+".wav")
	args := []string{"-protocol_whitelist", "file", "-nostdin", "-y"}
	if offset > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", offset.Seconds()))
	}
	args = append(args,
		"-i", audio,
		"-af", silenceFilter,
		"-t", fmt.Sprintf("%.3f", detectWindow.Seconds()),
		"-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-f", "wav",
		out)

	res := t.runTee(ctx, t.cfg.FFmpegBinary, args, nil)
	if res.Err != nil || res.ExitCode != 0 {
		if cerr := ctx.Err(); cerr != nil {
			return "", withStderrTail(domain.Wrap(classify(res.ExitCode, cerr), "whispercpp: ffmpeg clip for language detection stopped", cerr), res)
		}
		t.cfg.Logger.WarnContext(ctx, "whispercpp: ffmpeg clip for language detection failed; using the audio as it is",
			"window", name, "exit_code", res.ExitCode, "error", res.Err)
		return "", nil
	}
	if info, err := os.Stat(out); err != nil || info.Size() < minClipBytes {
		return "", nil // all silence: nothing to detect on
	}
	return out, nil
}

// detectClip runs whisper-cli -dl on one WAV file and returns what it
// reports.
func (t *Transcriber) detectClip(ctx context.Context, wav, modelPath, windowName string) (detection, error) {
	scan := &languageScanner{}
	res := t.runTee(ctx, t.cfg.Binary, buildDetectArgs(t.cfg, wav, modelPath), scan)
	if res.Err != nil || res.ExitCode != 0 {
		code := classifyRun(res)
		return detection{}, withStderrTail(domain.Wrap(code, "whispercpp: whisper-cli language detection failed", res.Err), res)
	}

	lang, prob := scan.result()
	if !languageCodeRE.MatchString(lang) {
		return detection{}, withStderrTail(domain.New(domain.ErrCodeToolFailure,
			"whispercpp: whisper-cli did not report a usable language code"), res).
			WithDetails("detectedLanguage", lang)
	}
	t.cfg.Logger.DebugContext(ctx, "whispercpp: language detection window",
		"window", windowName, "language", lang, "probability", prob)
	return detection{Language: lang, Probability: prob, Window: windowName}, nil
}
