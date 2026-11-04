package whispercpp

import (
	"strconv"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// whisperDefaultBeamSize and whisperDefaultTemperature mirror whisper-cli's
// own --help defaults ("-bs N ... [5]", "-tp N ... [0.00]") — also
// domain.Options' own defaults (domain.DefaultOptions, domain.Normalize),
// though that constant is unexported there. buildArgs uses these to decide
// when -bs/-tp are worth spelling out at all: Options.Normalize always
// fills BeamSize (never zero once it reaches Transcribe) and Temperature
// defaults to 0, so passing them unconditionally would make every
// invocation's argv identical regardless of what the caller actually
// asked for. Omitting them when they already match whisper-cli's own
// default keeps a logged argv legible: seeing "-bs 8" means the caller
// asked for something non-default, seeing no -bs at all means they didn't.
const (
	whisperDefaultBeamSize    = 5
	whisperDefaultTemperature = 0.0
)

// buildArgs builds whisper-cli's argument vector for one transcription run,
// exactly as CONTRACT.md section 8 specifies (flag spellings verified live
// against the current whisper-cli --help — see whispercpp.go's package
// doc).
//
// lang is always a real language code, never "auto": Transcribe detects the
// language first (see detectLanguage) or takes the caller's hint. translate
// comes from domain.PlanOutput, never from Options.Task. -tr appears only
// when translate is true, so Spanish audio can never get it.
func buildArgs(cfg Config, req domain.TranscribeRequest, lang string, translate bool, modelPath, outBase string) []string {
	jsonFlag := "-oj"
	if req.Options.WordTimestamps {
		jsonFlag = "-ojf"
	}

	args := []string{
		"-m", modelPath,
		"-f", req.AudioPath,
		"-t", strconv.Itoa(cfg.Threads),
		jsonFlag,
		"-of", outBase,
		"-l", lang,
	}

	args = append(args, deviceArgs(cfg)...)

	if translate {
		args = append(args, "-tr")
	}
	if req.Options.InitialPrompt != "" {
		args = append(args, "--prompt", req.Options.InitialPrompt)
	}
	if req.Options.BeamSize > 0 && req.Options.BeamSize != whisperDefaultBeamSize {
		args = append(args, "-bs", strconv.Itoa(req.Options.BeamSize))
	}
	if req.Options.Temperature != whisperDefaultTemperature {
		args = append(args, "-tp", strconv.FormatFloat(req.Options.Temperature, 'f', -1, 64))
	}

	args = append(args, "--no-prints")
	return args
}

// buildDetectArgs builds the argument vector for the language detection
// run: -dl makes whisper-cli exit after it detects the language. The run
// reads the same audio file as the transcription run, writes no output
// file, and reports the language on stderr.
//
// It must NOT pass --no-prints. That flag turns off whisper.cpp's log, and
// the "auto-detected language" line is a log line (verified on v1.9.4, see
// testdata/detect_language_jfk_tiny_no_prints.stderr).
func buildDetectArgs(cfg Config, audioPath, modelPath string) []string {
	args := []string{
		"-m", modelPath,
		"-f", audioPath,
		"-t", strconv.Itoa(cfg.Threads),
		"-dl",
	}
	return append(args, deviceArgs(cfg)...)
}

// deviceArgs returns the flags that pick CPU or GPU for one run.
func deviceArgs(cfg Config) []string {
	if cfg.NoGPU {
		return []string{"--no-gpu"}
	}
	return []string{"--device", strconv.Itoa(cfg.GPUDevice)}
}
