package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRunToResponsePreservesOpenResultValue(t *testing.T) {
	response := runToResponse(db.AutopilotRun{Result: []byte(`true`)})
	if result, ok := response.Result.(bool); !ok || !result {
		t.Fatalf("result = %#v, want true", response.Result)
	}
}
