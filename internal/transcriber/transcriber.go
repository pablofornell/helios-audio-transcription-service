// Package transcriber holds documentation and (should any accumulate)
// helpers shared by the two Transcriber implementations,
// internal/transcriber/whispercpp and internal/transcriber/fake. It
// deliberately does not declare the Transcriber interface itself: that
// port lives in internal/app (see its ports.go), declared once and
// satisfied structurally by both implementations below, so that a leaf
// implementation package never needs to import internal/app and risk an
// import cycle.
//
// Both implementations satisfy the same shape:
//
//	func New(cfg Config) *Transcriber
//	func (t *Transcriber) Transcribe(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error)
//	func (t *Transcriber) Available(ctx context.Context) error
//
// whispercpp drives the whisper-cli subprocess (see its own package doc
// for the CLI surface it owns); fake is deterministic and returns a stable
// result derived from the request, for tests and local dev under
// HELIOS_TRANSCRIBER_KIND=fake — mirror BLUEPRINT's
// internal/downloader/fake.
package transcriber
