package handler

import (
	"errors"
	"net/http"
)

type CreatePromptLibraryVersionResponse struct {
	Item    PromptLibraryItemResponse    `json:"item"`
	Version PromptLibraryVersionResponse `json:"version"`
}

func writePromptLibraryCreateReplayError(w http.ResponseWriter, resource string, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different " + resource + " request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover "+resource+" request")
}
