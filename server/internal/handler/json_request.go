package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
)

func jsonObjectBytesOrDefault(w http.ResponseWriter, raw json.RawMessage, field string, fallback []byte) ([]byte, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fallback, true
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		writeError(w, http.StatusBadRequest, field+" must be a JSON object")
		return nil, false
	}
	return raw, true
}

func mustJSONBytes(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic("handler: marshal required JSON value: " + err.Error())
	}
	return raw
}
