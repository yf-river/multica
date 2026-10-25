package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfigDeclaresCommentDeleteKeepsReplies(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	recorder := httptest.NewRecorder()
	testHandler.GetConfig(recorder, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["comment_delete_keep_replies_supported"] != true {
		t.Fatalf("keep-replies capability = %v", response["comment_delete_keep_replies_supported"])
	}
}
