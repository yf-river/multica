package handler

import (
	"net/http"
)

func writeCommentCreateReplayError(w http.ResponseWriter, err error) {
	writeResourceCreateReplayError(
		w, err,
		"Idempotency-Key was already used with a different comment request",
		"failed to recover comment request",
	)
}
