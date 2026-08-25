package handler

import (
	"net/http"
)

type CreatePromptLibraryVersionResponse struct {
	Item    PromptLibraryItemResponse    `json:"item"`
	Version PromptLibraryVersionResponse `json:"version"`
}

func writePromptLibraryCreateReplayError(w http.ResponseWriter, resource string, err error) {
	writeResourceCreateReplayError(
		w, err,
		"Idempotency-Key was already used with a different "+resource+" request",
		"failed to recover "+resource+" request",
	)
}
