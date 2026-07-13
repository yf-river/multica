package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPromptEvaluationCaseDataResponsesRejectInvalidPersistedShapes(t *testing.T) {
	tests := []struct {
		name string
		call func()
	}{
		{"case variables", func() { promptEvaluationCaseToResponse(validCaseDataRow([]byte(`[]`), []byte(`[]`)), nil) }},
		{"case tags", func() { promptEvaluationCaseToResponse(validCaseDataRow([]byte(`{}`), []byte(`{}`)), nil) }},
		{"version metadata", func() {
			promptEvaluationDatasetVersionToResponse(db.PromptEvaluationDatasetVersion{Metadata: []byte(`[]`)})
		}},
		{"version row variables", func() { promptEvaluationDatasetVersionRowToResponse(validVersionDataRow([]byte(`[]`), []byte(`[]`))) }},
		{"version row tags", func() { promptEvaluationDatasetVersionRowToResponse(validVersionDataRow([]byte(`{}`), []byte(`{}`))) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered == nil {
					t.Fatal("invalid persisted shape did not panic")
				}
			}()
			test.call()
		})
	}
}

func validCaseDataRow(variables, tags []byte) db.PromptEvaluationCase {
	return db.PromptEvaluationCase{Variables: variables, ExpectedContains: []byte(`[]`), Input: []byte(`{}`), Expected: []byte(`{}`), Tags: tags}
}

func validVersionDataRow(variables, tags []byte) db.PromptEvaluationDatasetVersionRow {
	return db.PromptEvaluationDatasetVersionRow{Variables: variables, ExpectedContains: []byte(`[]`), Expected: []byte(`{}`), Tags: tags}
}
