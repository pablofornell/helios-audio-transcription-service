package httpapi

import (
	"net/http"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// getStorage reports available bytes on the volume holding uploaded audio.
func (h *handlers) getStorage(w http.ResponseWriter, r *http.Request) {
	if h.storage == nil {
		writeError(w, r, domain.New(domain.ErrCodeInternal, "storage information unavailable"))
		return
	}
	free, err := h.storage.FreeBytes()
	if err != nil {
		writeError(w, r, domain.Wrap(domain.ErrCodeInternal, "storage information unavailable", err))
		return
	}
	writeJSON(w, http.StatusOK, struct {
		FreeBytes uint64 `json:"freeBytes"`
	}{FreeBytes: free})
}
