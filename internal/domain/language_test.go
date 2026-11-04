package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPlanOutput(t *testing.T) {
	for _, tt := range []struct {
		name      string
		source    string
		wantOut   string
		translate bool
		wantTask  Task
	}{
		{"spanish stays spanish", "es", "es", false, TaskTranscribe},
		{"spanish is case-insensitive", " ES ", "es", false, TaskTranscribe},
		{"english stays english", "en", "en", false, TaskTranscribe},
		{"french is translated", "fr", "en", true, TaskTranslate},
		{"three-letter code is translated", "haw", "en", true, TaskTranslate},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanOutput(tt.source)
			assert.Equal(t, tt.wantOut, plan.OutputLanguage)
			assert.Equal(t, tt.translate, plan.Translate)
			assert.Equal(t, tt.wantTask, plan.Task())
		})
	}
}
