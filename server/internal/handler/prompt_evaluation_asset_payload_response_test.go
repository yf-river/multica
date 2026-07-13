package handler

import (
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPromptEvaluationAssetResponsePreservesUnknownPayloadFields(t *testing.T) {
	response := promptEvaluationAssetToResponse(db.PromptEvaluationAsset{Payload: []byte(`{"unknown":{"nested":true}}`)})
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal asset response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode asset response: %v", err)
	}
	payload, ok := decoded["payload"].(map[string]any)
	if !ok || payload["unknown"] == nil {
		t.Fatalf("payload was not preserved: %#v", decoded["payload"])
	}
}

func TestPromptEvaluationAssetResponseRejectsInvalidPersistedPayload(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("invalid persisted payload did not panic")
		}
	}()
	promptEvaluationAssetToResponse(db.PromptEvaluationAsset{Payload: []byte(`[]`)})
}
