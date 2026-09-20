package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestContentDispositionAttachment_NonASCIIFilename pins RFC 6266 handling.
// HTTP header values are ISO-8859-1, so writing a non-ASCII rune straight
// into filename= makes a browser read it back as Latin-1 and save
// "grabación.ogg" as "grabaciÃ³n.ogg". A Spanish, French or Japanese upload
// must download under its real name.
func TestContentDispositionAttachment_NonASCIIFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		filename     string
		wantContains []string
		wantAbsent   []string
	}{
		{
			name:         "pure ascii needs no extended parameter",
			filename:     "interview.mp3",
			wantContains: []string{`attachment; filename="interview.mp3"`},
			wantAbsent:   []string{"filename*"},
		},
		{
			name:     "spanish accents are percent-encoded in filename*",
			filename: "grabación.ogg",
			wantContains: []string{
				`filename="grabaci_n.ogg"`,
				`filename*=UTF-8''grabaci%C3%B3n.ogg`,
			},
		},
		{
			name:     "spaces and enye",
			filename: "Español ñandú.ogg",
			wantContains: []string{
				`filename*=UTF-8''Espa%C3%B1ol%20%C3%B1and%C3%BA.ogg`,
			},
		},
		{
			// The ASCII extension survives in the fallback, which matters:
			// a client that ignores filename* still saves something the OS
			// will open with the right application.
			name:     "non-ascii stem keeps its ascii extension in the fallback",
			filename: "日本語.wav",
			wantContains: []string{
				`filename="___.wav"`,
				`filename*=UTF-8''%E6%97%A5%E6%9C%AC%E8%AA%9E.wav`,
			},
		},
		{
			name:     "a name with no ascii at all still yields something openable",
			filename: "日本語",
			wantContains: []string{
				`filename="download"`,
				`filename*=UTF-8''%E6%97%A5%E6%9C%AC%E8%AA%9E`,
			},
		},
		{
			name:     "quotes cannot break out of the parameter",
			filename: `ev"il\.mp3`,
			wantContains: []string{
				`filename="ev\"il\\.mp3"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := contentDispositionAttachment(tt.filename)

			// Whatever we emit must be a legal header value: no raw bytes
			// above ASCII, which is the whole point of the fix.
			for i := 0; i < len(got); i++ {
				require.Lessf(t, got[i], byte(0x80),
					"header value must be ASCII-only, found byte %#x in %q", got[i], got)
			}
			for _, want := range tt.wantContains {
				require.Containsf(t, got, want, "header value was %q", got)
			}
			for _, absent := range tt.wantAbsent {
				require.NotContainsf(t, got, absent, "header value was %q", got)
			}
		})
	}
}
