package main

import "strings"

// This file is ported verbatim from the sibling ingestion service's own
// sanitize.go. There, sanitizeTitle turned a platform-probed video title
// into a safe filename component for SaveCopy; here it plays the same
// role for reveal.go's audio cache (turning a transcription's uploaded
// Audio.Filename into a safe path component) and for SaveTranscript
// (turning that same filename, minus its extension, into the stem of the
// saved transcript file). The sanitisation rules themselves — what makes a
// string safe to use as a single filesystem path component across both
// POSIX and Windows — do not depend on where the string came from, so
// nothing below needed to change.

// sanitizeTitleFallback is what sanitizeTitle returns when title is empty
// or sanitises down to nothing at all (e.g. a name that was nothing but
// control characters and punctuation). Both of this file's callers already
// uniquify against whatever exists at the destination, so a generic
// fallback buys no extra safety — it only needs to read sensibly in a
// Downloads folder.
const sanitizeTitleFallback = "Untitled"

// maxTitleBytes caps how many UTF-8 bytes of a sanitised title this file's
// callers will use as a filename component. 255 bytes is the tightest
// common filesystem limit for a single path component — but every caller
// appends more after this: an extension, and separately a uniquify suffix
// like " (9999)" (7 bytes) when a name is already taken. Capping well
// short of 255 leaves headroom for both without an already-at-the-limit
// title pushing the final name over the filesystem's own limit.
const maxTitleBytes = 200

// windowsHostileChars is every character Windows refuses in a filename —
// a strict superset of what any POSIX filesystem forbids (only '/' and NUL
// there) — because a file this program writes on Linux may well end up
// synced to, or opened from, a Windows machine later (a cloud-synced
// Downloads folder, a shared drive).
const windowsHostileChars = `<>:"/\|?*`

// windowsReservedNames is every device name Windows refuses to use as a
// filename stem, regardless of extension ("CON.txt" is exactly as reserved
// as bare "CON") — matched case-insensitively below, mirroring Windows'
// own rule. Matched against the stem only — the substring before the
// title's own first '.', if it has one — never the whole title.
var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// isHostileFormattingRune reports whether r is one of three families of
// invisible-or-reordering Unicode characters that, unlike an ordinary
// control character, carry no visible content of their own for a human
// reading the title:
//
//   - Bidirectional formatting controls (U+202A-U+202E, U+2066-U+2069):
//     these change how the characters around them render, not what they
//     are. U+202E RIGHT-TO-LEFT OVERRIDE is the classic filename-spoofing
//     trick — a title containing it can make a file manager display
//     "malware.mp4" as "malware.mp4" spelled out but rendered
//     back-to-front, e.g. disguising an executable's real extension.
//   - Zero-width and other invisible marks (U+200B-U+200F, U+2060,
//     U+FEFF): these render as nothing at all, so a title with one hidden
//     inside it looks, to a human, byte-for-byte identical to the same
//     title without it, yet would otherwise sanitise to a different
//     filename.
//   - The C1 control block (U+0080-U+009F): a second range of non-printing
//     control characters, less common than C0 but exactly as invisible.
//
// sanitizeTitle drops these entirely rather than mapping them to a space:
// none of them was ever visible content a human perceived as a separator,
// so turning one into a space would add a gap that was never there to
// see.
func isHostileFormattingRune(r rune) bool {
	switch {
	case r >= 0x80 && r <= 0x9f: // C1 controls
		return true
	case r >= 0x200b && r <= 0x200f: // ZWSP, ZWNJ, ZWJ, LRM, RLM
		return true
	case r >= 0x202a && r <= 0x202e: // LRE, RLE, PDF, LRO, RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
		return true
	case r == 0x2060: // WORD JOINER
		return true
	case r == 0xfeff: // BOM / ZERO WIDTH NO-BREAK SPACE
		return true
	default:
		return false
	}
}

// sanitizeTitle turns title — wholly untrusted input, whether an uploaded
// audio file's own name or (transitively) a platform's probed metadata —
// into a string safe to use as a single filesystem path component: no
// path separators or NUL, no C0 or C1 control characters, no
// bidirectional-override or zero-width/invisible characters (see
// isHostileFormattingRune), none of Windows' hostile punctuation, no
// leading dot (which would silently create a hidden file on any POSIX
// system), no trailing dot or space (both illegal in a Windows filename),
// no Windows-reserved-device-name stem before the title's own first dot
// regardless of what follows it, never longer than maxTitleBytes (and
// never split mid-rune when trimmed to it), and never empty — see
// sanitizeTitleFallback's own doc for what an empty or all-unsafe title
// becomes instead.
//
// Non-ASCII letters — a Japanese, Spanish or emoji title — are preserved
// exactly: this only strips what is actually unsafe on a filesystem, never
// anything merely "foreign" by some other measure.
//
// This alone does not guarantee the result is safe to write as-is: both of
// this file's callers still uniquify the final name against whatever
// already exists at the destination.
func sanitizeTitle(title string) string {
	// Map every forbidden or control character to a single space, rather
	// than dropping it outright — so "Before/After" does not collapse into
	// the unrelated-looking "BeforeAfter" — and let the whitespace-collapsing
	// pass right after this turn any run of these (plus real whitespace)
	// into a single space. NUL and the invisible-formatting runes
	// isHostileFormattingRune identifies are the exceptions: they are
	// dropped entirely, without leaving a space behind, since none of them
	// was ever visible content a human read as a separator to begin with.
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r == 0:
			return -1
		case isHostileFormattingRune(r):
			return -1
		case r < 0x20 || r == 0x7f:
			return ' '
		case strings.ContainsRune(windowsHostileChars, r):
			return ' '
		default:
			return r
		}
	}, title)

	// strings.Fields splits on any run of Unicode whitespace and discards
	// empty tokens, so joining it back with single spaces both collapses
	// internal whitespace runs and trims leading/trailing whitespace in one
	// pass.
	collapsed := strings.Join(strings.Fields(mapped), " ")

	// A leading run of dots would otherwise silently create a hidden file
	// on any POSIX system; a trailing run of dots or spaces is flatly
	// illegal in a Windows filename.
	trimmed := strings.TrimRight(strings.TrimLeft(collapsed, ". "), ". ")
	if trimmed == "" {
		return sanitizeTitleFallback
	}

	trimmed = truncateUTF8(trimmed, maxTitleBytes)
	// Truncating can expose a new trailing space (when the cut lands right
	// after one that used to be internal); it cannot expose a new trailing
	// dot, since no dot was ever adjacent to a cut point introduced here —
	// but trimming both again is no more expensive than trimming one, and
	// guards it regardless.
	trimmed = strings.TrimRight(trimmed, ". ")
	if trimmed == "" {
		return sanitizeTitleFallback
	}

	// Windows reserves the device name as the filename's stem — the part
	// before its first dot — regardless of what, if anything, follows that
	// dot: "CON.txt" is exactly as reserved as bare "CON".
	stem, rest := trimmed, ""
	if dot := strings.IndexByte(trimmed, '.'); dot >= 0 {
		stem, rest = trimmed[:dot], trimmed[dot:]
	}
	if windowsReservedNames[strings.ToUpper(stem)] {
		// Appending an underscore to the stem — not to the end of the whole
		// title — is the smallest change that stops the stem matching while
		// leaving both it and whatever followed the dot still obviously the
		// same: "CON.mp4" becomes "CON_.mp4", not the extension-mangling
		// "CON.mp4_".
		trimmed = stem + "_" + rest
	}

	return trimmed
}

// truncateUTF8 returns the longest prefix of s that is both valid UTF-8
// and at most maxBytes bytes long. A plain s[:maxBytes] can split a
// multi-byte rune in half, producing invalid UTF-8 (and, on some
// filesystems, an outright rejected filename) the moment a title's cutoff
// point lands inside a non-ASCII character — exactly the titles this is
// most needed for.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := 0
	for i := range s {
		// range over a string yields the byte index of each rune's start,
		// in increasing order, so the last one at or before maxBytes is the
		// longest valid, non-rune-splitting cut point.
		if i > maxBytes {
			break
		}
		cut = i
	}
	return s[:cut]
}

// stripExt returns name with its final extension (as filepath.Ext would
// report it) removed, or name unchanged if it has none. reveal.go's
// SaveTranscript uses it to turn an audio filename like "interview.m4a"
// into the stem "interview" before appending the requested transcript
// format's own extension.
func stripExt(name string) string {
	for i := len(name) - 1; i >= 0 && name[i] != '/'; i-- {
		if name[i] == '.' {
			if i == 0 {
				break // a leading dot is not an extension separator
			}
			return name[:i]
		}
	}
	return name
}
