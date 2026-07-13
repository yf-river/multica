package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPromptEvaluationExecutionResponsesRejectInvalidPersistedObjects(t *testing.T) {
	tests := []struct {
		name string
		call func()
	}{
		{"trial", func() {
			promptEvaluationTrialToResponse(db.PromptEvaluationTrial{Input: []byte(`[]`), Expected: []byte(`{}`), Output: []byte(`{}`), Evidence: []byte(`{}`)})
		}},
		{"case operation", func() {
			promptEvaluationCaseOperationToResponse(db.PromptEvaluationCaseOperation{Filter: []byte(`[]`), Input: []byte(`{}`), SampleCaseIds: []byte(`[]`)})
		}},
		{"evidence snapshot", func() {
			promptEvaluationEvidenceSnapshotToResponse(db.PromptEvaluationEvidenceSnapshot{Summary: []byte(`[]`), Evidence: []byte(`{}`)}, true)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered == nil {
					t.Fatal("invalid persisted execution object did not panic")
				}
			}()
			test.call()
		})
	}
}
