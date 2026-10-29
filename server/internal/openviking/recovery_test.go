package openviking

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoveredExtractionMarksJournalComplete(t *testing.T) {
	const uri = "viking://user/ida/sessions/original"
	recoveredMessages := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result any = map[string]any{}
		switch {
		case r.URL.Path == "/api/v1/tasks/failed":
			result = map[string]string{"status": "failed"}
		case r.URL.Path == "/api/v1/tasks/recovered":
			result = map[string]string{"status": "completed"}
		case r.URL.Path == "/api/v1/fs/ls":
			result = []any{}
			if r.URL.Query().Get("uri") == uri+"/history" {
				result = []any{map[string]any{"uri": uri + "/history/archive_001", "isDir": true}}
			}
		case r.URL.Path == "/api/v1/content/download":
			switch r.URL.Query().Get("uri") {
			case uri + "/history/archive_001/.meta.json":
				w.Write([]byte(`{"phase1":{"queue_message":{"task_id":"failed"}}}`))
			case uri + "/history/archive_001/messages.jsonl":
				w.Write([]byte(`{"role":"user","parts":[{"type":"text","text":"original"}],"source_message_ids":["source-1"]}` + "\n"))
			default:
				http.NotFound(w, r)
			}
			return
		case strings.HasSuffix(r.URL.Path, "/messages/batch"):
			var batch struct {
				Messages []Message `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
				t.Error(err)
			}
			recoveredMessages += len(batch.Messages)
			if len(batch.Messages) != 1 || batch.Messages[0].Sources[0] != "source-1" {
				t.Error("recovery lost original source")
			}
		case strings.HasSuffix(r.URL.Path, "/commit"):
			result = map[string]string{"task_id": "recovered"}
		case r.URL.Path == "/api/v1/sessions":
		default:
			t.Errorf("unexpected native request %s", r.URL.String())
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "result": result})
	}))
	defer upstream.Close()
	client := &Client{URL: upstream.URL, HTTP: upstream.Client()}
	state := SyncState{MemoryTasks: []string{"failed"}}
	if err := client.finishExtraction(context.Background(), Metadata{User: "ida"}, uri, &state); err != nil {
		t.Fatal(err)
	}
	if !state.Done || recoveredMessages != 1 {
		t.Fatalf("successful recovery remained pending: %+v messages=%d", state, recoveredMessages)
	}
}
