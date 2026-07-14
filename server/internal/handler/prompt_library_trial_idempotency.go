package handler

import (
	"net/http"
)

func writePromptLibraryTrialReplayError(w http.ResponseWriter, err error) {
	writeResourceCreateReplayError(
		w, err,
		"Idempotency-Key was already used with a different prompt trial request",
		"failed to recover prompt library trial request",
	)
}
