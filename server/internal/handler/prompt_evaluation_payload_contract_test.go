package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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

func TestPromptEvaluationCaseProjectionsUseCanonicalFieldsOnly(t *testing.T) {
	assertCanonical := func(t *testing.T, item map[string]any) {
		t.Helper()
		for _, key := range []string{"case_name", "variables", "expected_contains", "tags"} {
			if _, exists := item[key]; !exists {
				t.Fatalf("canonical field %q missing: %#v", key, item)
			}
		}
		for _, key := range []string{"name", "名称", "变量", "期望包含", "输入", "期望", "标签", "状态"} {
			if _, exists := item[key]; exists {
				t.Fatalf("retired field %q survived: %#v", key, item)
			}
		}
	}

	fromCase := promptEvaluationPayloadCasesFromCaseRows([]db.PromptEvaluationCase{{
		CaseName: "case", Source: "payload", Variables: []byte(`{}`),
		ExpectedContains: []byte(`[]`), Tags: []byte(`[]`),
	}})
	if len(fromCase) != 1 {
		t.Fatalf("case projection = %#v", fromCase)
	}
	assertCanonical(t, fromCase[0])

	fromVersion := promptEvaluationPayloadCasesFromDatasetVersionRows([]db.PromptEvaluationDatasetVersionRow{{
		RowName: "case", Variables: []byte(`{}`), ExpectedContains: []byte(`[]`), Tags: []byte(`[]`),
	}})
	if len(fromVersion) != 1 {
		t.Fatalf("version projection = %#v", fromVersion)
	}
	assertCanonical(t, fromVersion[0])
}

func TestPromptEvaluationCaseNormalizerDoesNotReadRetiredAliases(t *testing.T) {
	retired := normalizePromptEvaluationCase(0, map[string]any{
		"名称":   "retired name",
		"变量":   map[string]any{"title": "retired"},
		"期望包含": []any{"retired"},
		"标签":   []any{"retired"},
	})
	if retired.Name != "用例 1" || len(retired.Variables) != 0 || len(retired.ExpectedContains) != 0 || len(retired.Tags) != 0 {
		t.Fatalf("retired aliases were still consumed: %#v", retired)
	}

	current := normalizePromptEvaluationCase(0, map[string]any{
		"case_name":         "current name",
		"variables":         map[string]any{"title": "current"},
		"expected_contains": []any{"current"},
		"tags":              []any{"current"},
	})
	if current.Name != "current name" || current.Variables["title"] != "current" || len(current.ExpectedContains) != 1 || len(current.Tags) != 1 {
		t.Fatalf("canonical fields were not preserved: %#v", current)
	}
}
