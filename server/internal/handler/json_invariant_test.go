package handler

import (
	"strings"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestMustJSONBytesRejectsUnsupportedValues(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("unsupported JSON value did not panic")
		}
		if message := recovered.(string); !strings.Contains(message, "marshal required JSON value") {
			t.Fatalf("panic = %q", message)
		}
	}()

	mustJSONBytes(make(chan struct{}))
}

func TestPromptEvaluationPersistedJSONReadersRejectInvalidShapes(t *testing.T) {
	tests := []struct {
		name string
		call func()
	}{
		{"payload", func() { decodePayloadObject([]byte(`[]`)) }},
		{"prompt variables", func() { promptVariableDefaults([]byte(`{}`)) }},
		{"restore metadata", func() {
			promptEvaluationDatasetVersionRestoreMetadata(db.PromptEvaluationDatasetVersion{}, []byte(`[]`))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered == nil {
					t.Fatal("invalid persisted JSON did not panic")
				}
			}()
			test.call()
		})
	}
}
