package handler

import (
	"net/http"
)

func writeWorkspaceMemberCreateReplayError(w http.ResponseWriter, err error) {
	writeResourceCreateReplayError(
		w, err,
		"Idempotency-Key was already used with a different workspace member request",
		"failed to recover workspace member request",
	)
}
