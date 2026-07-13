package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRunToResponseRejectsInvalidPersistedTriggerPayload(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("invalid persisted trigger payload should panic")
		}
	}()

	runToResponse(db.AutopilotRun{TriggerPayload: []byte(`[]`)})
}

func TestRunToResponsePreservesOpenResultValue(t *testing.T) {
	response := runToResponse(db.AutopilotRun{Result: []byte(`true`)})
	if result, ok := response.Result.(bool); !ok || !result {
		t.Fatalf("result = %#v, want true", response.Result)
	}
}
