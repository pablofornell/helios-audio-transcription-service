package audio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// pcmBytesPerSecond is the exact byte rate of the WAV Normalize produces:
// 16000 samples/sec, 1 channel, 2 bytes/sample (see normalizeArgs'
// -ac/-ar/-c:a flags). maxOutputBytes uses it to size -fs.
const pcmBytesPerSecond = 16000 * 2

// outputByteSlack is added on top of the raw PCM byte count -fs allows, for
// the WAV container's own header/chunk overhead — a few dozen bytes in
// practice, but generous slack here costs nothing and avoids -fs cutting
// off a legitimately-sized clip mid-header.
const outputByteSlack = 1 << 16 // 64 KiB

// maxOutputBytes bounds Normalize's output file size independently of
// -t/maxDuration: a belt-and-braces ceiling for the case where a
// container's duration accounting and -t somehow disagree (e.g. a variable
// bitrate stream ffmpeg mis-estimates), so a single Normalize call can
// never produce more than a bounded multiple of what a legitimate
// MaxDuration-length clip would.
func maxOutputBytes(maxDuration time.Duration) int64 {
	return int64(maxDuration.Seconds()*pcmBytesPerSecond) + outputByteSlack
}

// normalizeArgs are ffmpeg's flags for producing the 16 kHz mono 16-bit PCM
// WAV whisper.cpp requires, exactly as the contract specifies:
//   - -protocol_whitelist file: see probeArgs' doc comment for why — the
//     same defense-in-depth against a container indirecting through a
//     non-file protocol applies here, where ffmpeg does the actual
//     decoding.
//   - -nostdin: this process never has (or should read from) a controlling
//     terminal; without it, ffmpeg can block waiting for keyboard input on
//     an unexpected prompt (e.g. "overwrite?") instead of failing fast.
//   - -y: overwrite outPath without prompting — belt and braces alongside
//     -nostdin, and also lets Normalize reuse a temp file it just created
//     via os.CreateTemp (see tempWAVPath).
//   - -vn -map 0:a:0: drop any video stream entirely and select exactly the
//     first audio stream, so a container with multiple audio tracks (dub
//     tracks, commentary) does not leave ffmpeg to guess or mux more than
//     one.
//   - -ac 1 -ar 16000 -c:a pcm_s16le -f wav: whisper.cpp's own required
//     input format.
//   - -t <maxDuration> -fs <maxOutputBytes(maxDuration)>: a hard ceiling on
//     how much ffmpeg will ever write, independent of what Probe's own
//     duration check saw. A container can self-report a duration that has
//     nothing to do with how much media actually decodes out of it (e.g. an
//     HLS/DASH playlist's EXTINF tag, or a stream ffprobe simply
//     mis-estimated) — Probe's MaxDuration check alone is not a ceiling on
//     Normalize's own output, only on what it was told going in. -t stops
//     ffmpeg from decoding past maxDuration of output regardless of what it
//     was told the source's length is; -fs is the same guarantee measured
//     in bytes, catching the case where -t is somehow defeated.
func normalizeArgs(srcAbs, outPath string, maxDuration time.Duration) []string {
	return []string{
		"-protocol_whitelist", "file",
		"-nostdin", "-y",
		"-i", srcAbs,
		"-vn",
		"-map", "0:a:0",
		"-ac", "1",
		"-ar", "16000",
		"-c:a", "pcm_s16le",
		"-t", fmt.Sprintf("%.3f", maxDuration.Seconds()),
		"-fs", strconv.FormatInt(maxOutputBytes(maxDuration), 10),
		"-f", "wav",
		outPath,
	}
}

// Normalize implements app.AudioProcessor.Normalize (see internal/app's
// ports.go): it shells out to ffmpeg to convert srcAbs into dstAbs, a 16
// kHz mono 16-bit PCM WAV. It writes to a temp file next to dstAbs and
// renames on success, so a run that fails partway through (a bad codec, a
// killed process, a full disk) never leaves a half-written WAV at dstAbs
// for whisper.cpp to be handed next.
//
// Free space is checked again here, not only at upload time (that check —
// internal/app's Service.Submit — is Storage-store-relative and runs
// before the source is even written): time has passed since then, other
// jobs may have filled the disk in the meantime, and Normalize is about to
// write a new file of its own.
func (p *Processor) Normalize(ctx context.Context, srcAbs, dstAbs string) error {
	if err := p.checkFreeSpace(dstAbs); err != nil {
		return err
	}

	tmpPath, err := tempWAVPath(dstAbs)
	if err != nil {
		return domain.Wrap(classifyLocalErr(err), "audio: create temp output file", err)
	}
	// ok tracks whether tmpPath was successfully renamed into place; if
	// Normalize returns before that, the temp file (however far ffmpeg got
	// writing it) must not be left behind.
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()

	res := p.run(ctx, p.cfg.FFmpegBin, normalizeArgs(srcAbs, tmpPath, p.cfg.MaxDuration))
	if res.Err != nil || res.ExitCode != 0 {
		code := classify(res.Stderr, res.ExitCode, res.Err)
		return withStderrTail(domain.Wrap(code, "audio: ffmpeg normalize failed", res.Err), res)
	}

	if err := os.Rename(tmpPath, dstAbs); err != nil {
		return domain.Wrap(classifyLocalErr(err), "audio: rename normalized wav into place", err)
	}
	ok = true
	return nil
}

// checkFreeSpace rejects Normalize with domain.ErrCodeDiskFull when the
// volume backing dstAbs has less than p.cfg.MinFreeBytes free space.
// MinFreeBytes of zero (the zero value) disables the check — matching
// Storage.MinFreeBytes' own "0 disables the floor" convention (see
// internal/config) — since a Processor built without an explicit floor
// (e.g. most unit tests) has no threshold to enforce.
func (p *Processor) checkFreeSpace(dstAbs string) error {
	if p.cfg.MinFreeBytes == 0 {
		return nil
	}
	var st unix.Statfs_t
	if err := unix.Statfs(filepath.Dir(dstAbs), &st); err != nil {
		return domain.Wrap(classifyLocalErr(err), "audio: statfs before normalize", err)
	}
	// Bavail and Bsize are both unsigned on linux/amd64 but signed on some
	// other unix targets; the explicit conversions keep this correct
	// either way — mirrors internal/storage/localfs's own FreeBytes.
	free := uint64(st.Bavail) * uint64(st.Bsize)
	if free < p.cfg.MinFreeBytes {
		return domain.New(domain.ErrCodeDiskFull, "audio: insufficient free storage space to normalize audio").
			WithDetails("freeBytes", free).
			WithDetails("minFreeBytes", p.cfg.MinFreeBytes)
	}
	return nil
}

// tempWAVPath reserves a unique filename next to dstAbs (in the same
// directory, so the final os.Rename is same-filesystem and therefore
// atomic) and returns it unopened-but-created — os.CreateTemp is used only
// to obtain a collision-free name via its own randomness; the file it
// creates is then closed immediately so ffmpeg's own -y can create/write it
// as a normal file.
func tempWAVPath(dstAbs string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(dstAbs), ".normalize-*.wav.tmp")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}
