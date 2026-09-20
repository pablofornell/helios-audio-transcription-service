package whispercpp

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// Available implements app.Transcriber.Available (see internal/app's
// ports.go): it checks that Config.Binary resolves on PATH (or is a
// usable absolute/relative path — exec.LookPath handles both), that
// Config.ModelDir exists and can be listed, and that Config.DefaultModel's
// weight file is actually present in it. It stops short of checking any
// other model: Transcribe itself is what discovers a missing non-default
// model, per-request, since Available has no per-request model name to
// check against — but the default is the one model every deployment must
// have, and an empty ModelDir is otherwise indistinguishable at this check
// from a correctly-populated one (both are "readable").
func (t *Transcriber) Available(ctx context.Context) error {
	_ = ctx // no I/O here blocks long enough to need a deadline of its own
	if _, err := exec.LookPath(t.cfg.Binary); err != nil {
		return domain.Wrap(domain.ErrCodeInternal,
			fmt.Sprintf("whispercpp: binary %q not found", t.cfg.Binary), err)
	}
	if _, err := os.ReadDir(t.cfg.ModelDir); err != nil {
		return domain.Wrap(domain.ErrCodeModelUnavailable,
			fmt.Sprintf("whispercpp: model dir %q not readable", t.cfg.ModelDir), err)
	}
	if t.cfg.DefaultModel == "" {
		return nil
	}
	modelPath, err := t.modelPath(t.cfg.DefaultModel)
	if err != nil {
		return err
	}
	if _, err := os.Stat(modelPath); err != nil {
		return domain.Wrap(domain.ErrCodeModelUnavailable,
			fmt.Sprintf("whispercpp: default model %q is not present on disk", t.cfg.DefaultModel), err).
			WithDetails("model", t.cfg.DefaultModel)
	}
	return nil
}
