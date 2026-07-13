package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTaskTraceEventToResponseRejectsNonObjectMetadata(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`null`), []byte(`[]`), []byte(`"metadata"`)} {
		if _, err := taskTraceEventToResponse(db.TaskTraceEvent{Metadata: raw}); err == nil {
			t.Fatalf("taskTraceEventToResponse metadata=%s expected an error", raw)
		}
	}
}
