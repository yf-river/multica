package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPromptEvaluationResultResponsesRejectInvalidPersistedObjects(t *testing.T) {
	tests := []struct {
		name string
		call func()
	}{
		{name: "run metrics", call: func() {
			promptEvaluationRunToResponse(db.PromptEvaluationRun{Metrics: []byte(`[]`), Evidence: []byte(`{}`)})
		}},
		{name: "run evidence", call: func() {
			promptEvaluationRunToResponse(db.PromptEvaluationRun{Metrics: []byte(`{}`), Evidence: []byte(`[]`)})
		}},
		{name: "candidate failure summary", call: func() {
			promptEvaluationOptimizationCandidateToResponse(db.PromptEvaluationOptimizationCandidate{SourceFailureSummary: []byte(`[]`), SourcePromptSnapshot: []byte(`{}`), Metrics: []byte(`{}`)})
		}},
		{name: "candidate prompt snapshot", call: func() {
			promptEvaluationOptimizationCandidateToResponse(db.PromptEvaluationOptimizationCandidate{SourceFailureSummary: []byte(`{}`), SourcePromptSnapshot: []byte(`[]`), Metrics: []byte(`{}`)})
		}},
		{name: "candidate metrics", call: func() {
			promptEvaluationOptimizationCandidateToResponse(db.PromptEvaluationOptimizationCandidate{SourceFailureSummary: []byte(`{}`), SourcePromptSnapshot: []byte(`{}`), Metrics: []byte(`[]`)})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered == nil {
					t.Fatal("invalid persisted object did not panic")
				}
			}()
			test.call()
		})
	}
}
