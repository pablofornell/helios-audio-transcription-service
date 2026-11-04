package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// The result shows the detected language and the output language apart.
func TestResultFromDomain_carriesDetectedAndOutputLanguage(t *testing.T) {
	dto := resultFromDomain(&domain.Result{Language: "fr", OutputLanguage: "en", Task: domain.TaskTranslate})
	require.NotNil(t, dto)

	raw, err := json.Marshal(dto)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "fr", got["language"])
	assert.Equal(t, "en", got["outputLanguage"])
}

// A result stored before outputLanguage existed has no such key.
func TestResultFromDomain_omitsEmptyOutputLanguage(t *testing.T) {
	raw, err := json.Marshal(resultFromDomain(&domain.Result{Language: "en"}))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "outputLanguage")
}
