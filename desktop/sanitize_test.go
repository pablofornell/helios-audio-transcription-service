package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{
			name:  "ordinary title is untouched",
			title: "My Vacation Video",
			want:  "My Vacation Video",
		},
		{
			name:  "path separators become spaces, not glued text",
			title: "Before/After\\Comparison",
			want:  "Before After Comparison",
		},
		{
			name:  "NUL is dropped without leaving a gap",
			title: "abc\x00def",
			want:  "abcdef",
		},
		{
			name:  "control characters become spaces",
			title: "line1\nline2\ttabbed",
			want:  "line1 line2 tabbed",
		},
		{
			name:  "the full windows-hostile punctuation set becomes spaces",
			title: `a<b>c:d"e/f\g|h?i*j`,
			want:  "a b c d e f g h i j",
		},
		{
			name:  "trailing dots and spaces are stripped",
			title: "Talk... ",
			want:  "Talk",
		},
		{
			name:  "leading dots are stripped so no hidden file is created",
			title: "...and justice for all",
			want:  "and justice for all",
		},
		{
			name:  "runs of internal whitespace collapse to one space",
			title: "Too      Many     Spaces",
			want:  "Too Many Spaces",
		},
		{
			name:  "a title that is nothing but forbidden characters sanitises to the fallback",
			title: "///???",
			want:  sanitizeTitleFallback,
		},
		{
			name:  "an empty title falls back",
			title: "",
			want:  sanitizeTitleFallback,
		},
		{
			name:  "a whitespace-only title falls back",
			title: "   \t  ",
			want:  sanitizeTitleFallback,
		},
		{
			name:  "windows reserved device name CON is disambiguated",
			title: "CON",
			want:  "CON_",
		},
		{
			name:  "windows reserved device names are matched case-insensitively",
			title: "com1",
			want:  "com1_",
		},
		{
			name:  "a title that merely contains a reserved name is untouched",
			title: "CONcert Footage",
			want:  "CONcert Footage",
		},
		{
			// The reserved-name guard matches the stem before the title's
			// own first dot, not the whole title: "CON.mp4" is exactly as
			// reserved as bare "CON" (see windowsReservedNames' own doc),
			// because SaveCopy's caller appends a real extension after
			// whatever this returns, so a title that already contains a dot
			// produces that same stem in the final filename on disk.
			name:  "windows reserved device name with a dotted suffix is disambiguated at the stem",
			title: "CON.mp4",
			want:  "CON_.mp4",
		},
		{
			name:  "windows reserved device name with a dotted suffix is matched case-insensitively",
			title: "con.MP4",
			want:  "con_.MP4",
		},
		{
			name:  "another reserved device name with a dotted suffix is disambiguated at the stem",
			title: "nul.txt",
			want:  "nul_.txt",
		},
		{
			name:  "a reserved-looking device name with a dotted suffix is disambiguated at the stem",
			title: "COM9.jpg",
			want:  "COM9_.jpg",
		},
		{
			// COM10 is not one of Windows' reserved names (only COM1-COM9
			// are) and must not be over-matched just because it starts with
			// "COM" followed by a digit.
			name:  "a name that merely starts with a reserved stem's prefix is untouched",
			title: "COM10.txt",
			want:  "COM10.txt",
		},
		{
			// CONSTANTINE starts with "CON" but its stem, taken whole, is not
			// itself a reserved name, and must not be over-matched.
			name:  "a name that merely starts with a reserved word is untouched, dotted suffix",
			title: "CONSTANTINE.mp4",
			want:  "CONSTANTINE.mp4",
		},
		{
			name:  "spanish accented letters are preserved, not ascii-folded",
			title: "Canción de Amor",
			want:  "Canción de Amor",
		},
		{
			name:  "japanese characters are preserved, not stripped",
			title: "夏の思い出",
			want:  "夏の思い出",
		},
		{
			name:  "emoji are preserved, not stripped",
			title: "Best Trip Ever 🎉🌴😎",
			want:  "Best Trip Ever 🎉🌴😎",
		},
		{
			// U+202E RIGHT-TO-LEFT OVERRIDE is the classic filename-spoofing
			// trick: left unstripped, a file manager would render this
			// title's tail back-to-front, disguising what looks like a
			// ".mp4" file's real name/extension order.
			name:  "a right-to-left override is stripped, not preserved",
			title: "Test\u202egnp.mp4tseT",
			want:  "Testgnp.mp4tseT",
		},
		{
			name:  "a left-to-right override is stripped",
			title: "\u202dTest",
			want:  "Test",
		},
		{
			// A zero-width space hidden inside an otherwise-ordinary title
			// must not survive to make it sanitise differently from the same
			// title without one: a human reading either version sees
			// identical text.
			name:  "a zero-width space is stripped, not turned into a visible gap",
			title: "Te\u200bst",
			want:  "Test",
		},
		{
			name:  "a zero-width joiner is stripped",
			title: "Te\u200dst",
			want:  "Test",
		},
		{
			name:  "a byte-order-mark / zero-width-no-break-space is stripped",
			title: "\ufeffTest",
			want:  "Test",
		},
		{
			name:  "a C1 control character is stripped",
			title: "Te\u0085st",
			want:  "Test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeTitle(tt.title); got != tt.want {
				t.Errorf("sanitizeTitle(%q) = %q, want %q", tt.title, got, tt.want)
			}
		})
	}
}

// TestSanitizeTitle_LengthCapTruncatesOnRuneBoundary proves the length cap
// never splits a multi-byte rune in half, using three-byte-per-rune
// Japanese text specifically because it makes an off-by-one truncation
// point land mid-character on the very first attempt, unlike ASCII input
// (where every cut point is trivially a rune boundary) or two-byte runes
// (where only every other cut point is wrong).
func TestSanitizeTitle_LengthCapTruncatesOnRuneBoundary(t *testing.T) {
	rune3 := "夏" // exactly 3 bytes in UTF-8
	title := strings.Repeat(rune3, 200)
	if len(title) <= maxTitleBytes {
		t.Fatalf("test setup: title is only %d bytes, want more than maxTitleBytes (%d)", len(title), maxTitleBytes)
	}

	got := sanitizeTitle(title)

	if !utf8.ValidString(got) {
		t.Fatalf("sanitizeTitle produced invalid UTF-8: %q", got)
	}
	if len(got) > maxTitleBytes {
		t.Fatalf("sanitizeTitle result is %d bytes, want at most %d", len(got), maxTitleBytes)
	}
	// Every byte of the result should be a whole number of 3-byte runes —
	// if truncation had split one, the trailing partial bytes would either
	// be missing from the count or utf8.ValidString above would already
	// have failed.
	if len(got)%len(rune3) != 0 {
		t.Errorf("sanitizeTitle result is %d bytes, not a whole number of runes", len(got))
	}
}

// TestSanitizeTitle_LongASCIITitleIsCapped covers the plain, common case
// of the same cap: a long but entirely ordinary ASCII title is shortened,
// not rejected or replaced with the fallback.
func TestSanitizeTitle_LongASCIITitleIsCapped(t *testing.T) {
	title := strings.Repeat("a", maxTitleBytes+50)
	got := sanitizeTitle(title)
	if len(got) > maxTitleBytes {
		t.Fatalf("sanitizeTitle result is %d bytes, want at most %d", len(got), maxTitleBytes)
	}
	if got == sanitizeTitleFallback {
		t.Fatalf("a long, otherwise-ordinary title should be truncated, not replaced with the fallback")
	}
}

func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		maxBytes int
		want     string
	}{
		{name: "under the limit is untouched", s: "hello", maxBytes: 10, want: "hello"},
		{name: "exactly at the limit is untouched", s: "hello", maxBytes: 5, want: "hello"},
		{name: "ASCII truncates cleanly", s: "hello world", maxBytes: 5, want: "hello"},
		{
			// "é" is 2 bytes (0xC3 0xA9); a cap of 2 fits "h" (1 byte) but
			// not "h"+"é" (3 bytes), so it must stop after "h" rather than
			// emit a truncated, invalid second byte of "é".
			name:     "does not split a multi-byte rune",
			s:        "héllo",
			maxBytes: 2,
			want:     "h",
		},
		{name: "zero bytes yields empty", s: "hello", maxBytes: 0, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateUTF8(tt.s, tt.maxBytes)
			if got != tt.want {
				t.Errorf("truncateUTF8(%q, %d) = %q, want %q", tt.s, tt.maxBytes, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncateUTF8(%q, %d) = %q is not valid UTF-8", tt.s, tt.maxBytes, got)
			}
		})
	}
}

// TestStripExt covers stripExt (sanitize.go), added for this service's own
// SaveTranscript, which has no sibling equivalent.
func TestStripExt(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "ordinary extension is removed", in: "interview.m4a", want: "interview"},
		{name: "no extension is untouched", in: "interview", want: "interview"},
		{name: "multiple dots strip only the last extension", in: "my.interview.wav", want: "my.interview"},
		{name: "a leading dot is not treated as an extension separator", in: ".hidden", want: ".hidden"},
		{name: "empty string is untouched", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripExt(tt.in); got != tt.want {
				t.Errorf("stripExt(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
