package handler

import (
	"net/http"
)

func writeAgentPlaygroundCreateReplayError(w http.ResponseWriter, err error) {
	writeResourceCreateReplayError(
		w, err,
		"Idempotency-Key was already used with a different agent playground request",
		"failed to recover agent playground request",
	)
}
