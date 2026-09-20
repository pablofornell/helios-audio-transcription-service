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

// buildArgs builds whisper-cli's argument vector for one Transcribe call,
// exactly as CONTRACT.md section 8 specifies (flag spellings verified live
// against the current whisper-cli --help — see whispercpp.go's package
// doc).
func buildArgs(cfg Config, req domain.TranscribeRequest, modelPath, outBase string) []string {
	lang := req.Options.Language
	if req.Options.AutoDetectLanguage() {
		lang = "auto"
	}

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

	if req.Options.Task == domain.TaskTranslate {
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
