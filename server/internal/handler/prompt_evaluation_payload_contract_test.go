package handler

import "testing"

func TestPromptEvaluationPayloadKeepsOneCaseCollection(t *testing.T) {
	cases := []map[string]any{{"case_name": "current"}}
	payload := promptEvaluationPayloadWithCases(map[string]any{
		"用例":     []any{map[string]any{"名称": "retired"}},
		"custom": "preserved",
	}, cases)
	if _, exists := payload["用例"]; exists {
		t.Fatal("retired case alias survived current payload assembly")
	}
	if payload["custom"] != "preserved" || len(promptEvaluationCases(payload)) != 1 {
		t.Fatalf("current payload fields were not preserved: %#v", payload)
	}

	normalized := normalizePromptEvaluationPayloadObject(map[string]any{
		"cases": cases,
		"用例":    []any{map[string]any{"名称": "retired"}},
	})
	if _, exists := normalized["用例"]; exists {
		t.Fatal("retired case alias survived normalization")
	}
}
