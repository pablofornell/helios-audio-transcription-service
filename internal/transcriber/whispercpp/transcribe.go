package whispercpp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// modelFilePrefix and modelFileSuffix build the ggml-<model>.bin naming
// convention every whisper.cpp model download uses (see
// models/download-ggml-model.sh upstream).
const (
	modelFilePrefix = "ggml-"
	modelFileSuffix = ".bin"
)

// modelPath returns the absolute path a model named model should live at
// under t.cfg.ModelDir, or an error if that path would not actually be
// inside ModelDir.
//
// domain.Options.Validate's modelNamePattern already rejects any model
// name containing "/" (or anything else outside a small, path-safe
// charset) before a request ever reaches here, which on its own makes
// escaping ModelDir via this concatenation impossible. This check is
// nonetheless a second, independent gate — this package must not rely on
// every caller having gone through Validate first (Transcribe is a public
// method), and a future change to modelFilePrefix/modelFileSuffix or the
// join itself should not be able to silently reopen the escape.
func (t *Transcriber) modelPath(model string) (string, error) {
	dir := filepath.Clean(t.cfg.ModelDir)
	joined := filepath.Join(dir, modelFilePrefix+model+modelFileSuffix)
	rel, err := filepath.Rel(dir, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", domain.New(domain.ErrCodeInvalidInput, "whispercpp: model name is invalid").
			WithDetails("field", "model")
	}
	return joined, nil
}

// Transcribe implements app.Transcriber.Transcribe (see internal/app's
// ports.go): it locates the requested model, runs whisper-cli in a
// per-job temp directory created next to req.AudioPath (inside the store
// root the caller's AudioPath already lives under — never /tmp directly,
// per CONTRACT.md section 11's convention), and parses its JSON output
// into a domain.Result.
func (t *Transcriber) Transcribe(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
	if req.Options.Model == "" {
		// Options.Validate documents an empty Model as meaning "the
		// configured default", resolved by app.Service before a job is
		// ever enqueued — TranscribeRequest.Options reaching here should
		// therefore always carry a concrete model name. This guard is
		// defense in depth, not an expected path: this package has no
		// default of its own to fall back on (Config carries no
		// DefaultModel field — that knob lives in app/config, not here).
		return nil, domain.New(domain.ErrCodeInvalidInput, "whispercpp: options.Model must already be resolved").
			WithDetails("field", "model")
	}

	modelPath, err := t.modelPath(req.Options.Model)
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(modelPath); statErr != nil {
		// Details deliberately carries only the model name, not the
		// resolved absolute path: echoing modelPath back to a caller turns
		// this error into a file-existence oracle for wherever ModelDir
		// (or a caller who found a way around modelPath's own guard)
		// happens to point.
		return nil, domain.Wrap(domain.ErrCodeModelUnavailable,
			fmt.Sprintf("whispercpp: model %q is not present on disk", req.Options.Model), statErr).
			WithDetails("model", req.Options.Model)
	}

	outDir, err := os.MkdirTemp(filepath.Dir(req.AudioPath), ".whisper-out-")
	if err != nil {
		return nil, domain.Wrap(domain.ErrCodeInternal, "whispercpp: create per-job temp dir", err)
	}
	defer func() { _ = os.RemoveAll(outDir) }()

	outBase := filepath.Join(outDir, "transcript")
	args := buildArgs(t.cfg, req, modelPath, outBase)

	res := t.run(ctx, args)
	if res.Err != nil || res.ExitCode != 0 {
		code := classifyRun(res)
		return nil, withStderrTail(domain.Wrap(code, "whispercpp: whisper-cli failed", res.Err), res)
	}

	data, err := os.ReadFile(outBase + ".json")
	if err != nil {
		return nil, withStderrTail(domain.Wrap(domain.ErrCodeToolFailure, "whispercpp: read whisper-cli output JSON", err), res)
	}

	result, err := parseWhisperJSON(data, req.Options)
	if err != nil {
		return nil, withStderrTail(domain.Wrap(domain.ErrCodeToolFailure, "whispercpp: parse whisper-cli output JSON", err), res)
	}

	result.Model = req.Options.Model
	result.Task = req.Options.Task
	if !req.Options.AutoDetectLanguage() && result.Language == "" {
		// A forced language always echoes back as itself even if, for
		// whatever reason, whisper-cli's own "result".language came back
		// empty (e.g. an older build's JSON omitting it under -l other
		// than auto) — Result.Language documents this as "the detected...
		// or forced... code whisper-cli actually used".
		result.Language = req.Options.Language
	}
	return result, nil
}
