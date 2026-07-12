package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
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
		"名称":       "retired name",
		"变量":       map[string]any{"title": "retired"},
		"期望包含":     []any{"retired"},
		"标签":       []any{"retired"},
		"input":    map[string]any{"retired": true},
		"expected": map[string]any{"retired": true},
	})
	if retired.Name != "用例 1" || len(retired.Variables) != 0 || len(retired.ExpectedContains) != 0 || len(retired.Tags) != 0 {
		t.Fatalf("retired aliases were still consumed: %#v", retired)
	}
	if _, exists := retired.Input["原始输入"]; exists {
		t.Fatalf("retired input survived normalization: %#v", retired.Input)
	}
	if _, exists := retired.Expected["原始期望"]; exists {
		t.Fatalf("retired expected value survived normalization: %#v", retired.Expected)
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

func TestPromptEvaluationDatasetLinksUseCanonicalFieldsOnly(t *testing.T) {
	payload := map[string]any{
		"linked_dataset_ids": []any{"dataset-1"},
		"linked_dataset_versions": []any{map[string]any{
			"dataset_version_id": "version-1",
			"dataset_asset_id":   "dataset-2",
			"dataset_name":       "Current Dataset",
		}},
		"数据集版本":   []any{map[string]any{"数据集版本ID": "retired-version"}},
		"关联数据集ID": "retired-dataset",
	}
	refs := promptEvaluationExplicitDatasetVersionRefs(payload)
	if len(refs) != 1 || refs[0].DatasetVersionID != "version-1" || refs[0].DatasetAssetID != "dataset-2" || refs[0].DatasetName != "Current Dataset" {
		t.Fatalf("canonical refs = %#v", refs)
	}
	ids := promptEvaluationLinkedDatasetIDs(payload)
	if len(ids) != 2 || ids[0] != "dataset-1" || ids[1] != "dataset-2" {
		t.Fatalf("canonical ids = %#v", ids)
	}
	if refs := promptEvaluationExplicitDatasetVersionRefs(map[string]any{"数据集版本": payload["数据集版本"]}); len(refs) != 0 {
		t.Fatalf("retired top-level alias was still consumed: %#v", refs)
	}
}

func TestPromptEvaluationMetricsAndExperimentDimensionsHaveDistinctContracts(t *testing.T) {
	payload := map[string]any{
		"metric_contract":       []any{"case_count", "pass_rate"},
		"metric_notes":          []any{"按当前快照统计"},
		"experiment_dimensions": []any{"命中率", map[string]any{"name": "中文一致性", "weight": 2}},
		"实验维度":                  []any{"retired"},
		"指标口径":                  []any{"retired"},
	}
	dimensions := promptEvaluationExperimentDimensions(payload)
	if len(dimensions) != 2 || dimensions[0].Name != "命中率" || dimensions[1].Name != "中文一致性" {
		t.Fatalf("canonical experiment dimensions = %#v", dimensions)
	}
	profile := promptEvaluationAssetProfileFromPayload(mustJSONBytes(payload), pgtype.UUID{}, promptEvaluationAssetTestSuite)
	if profile.EvaluationDimensionCount != 2 || profile.ExperimentDimensionCount != 2 {
		t.Fatalf("profile metric=%d experiment=%d", profile.EvaluationDimensionCount, profile.ExperimentDimensionCount)
	}
	if legacy := promptEvaluationExperimentDimensions(map[string]any{
		"实验维度":            []any{"retired"},
		"metric_contract": []any{"must-not-be-a-score"},
	}); len(legacy) != 0 {
		t.Fatalf("retired or metric fields became experiment dimensions: %#v", legacy)
	}
}

func TestPromptEvaluationExperimentContextUsesCanonicalFieldsOnly(t *testing.T) {
	payload := map[string]any{
		"experiment_target":     "current target",
		"baseline_output":       "current baseline",
		"experiment_dimensions": []any{"命中率"},
		"实验对象":                  "retired target",
		"基线输出":                  "retired baseline",
	}
	dimensions := promptEvaluationExperimentDimensions(payload)
	if len(dimensions) != 1 || dimensions[0].ExperimentTarget != "current target" || dimensions[0].BaselineOutput != "current baseline" {
		t.Fatalf("canonical context = %#v", dimensions)
	}
	legacy := promptEvaluationExperimentDimensions(map[string]any{
		"experiment_dimensions": []any{"命中率"},
		"实验对象":                  "retired target",
		"基线输出":                  "retired baseline",
	})
	if len(legacy) != 1 || legacy[0].ExperimentTarget != "" || legacy[0].BaselineOutput != "" {
		t.Fatalf("retired context aliases were consumed: %#v", legacy)
	}
}

func TestPromptEvaluationPayloadRejectsMalformedAgentSelection(t *testing.T) {
	for _, raw := range []string{`{"agent_id":{}}`, `{"agent_id":"  "}`} {
		w := httptest.NewRecorder()
		if _, ok := promptEvaluationPayloadField(w, json.RawMessage(raw), "payload", false); ok {
			t.Fatalf("malformed agent selection was accepted: %s", raw)
		}
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
		}
	}
}
