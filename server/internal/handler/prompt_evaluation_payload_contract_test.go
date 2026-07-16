package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPromptEvaluationPayloadPreservesCustomFields(t *testing.T) {
	cases := []map[string]any{{"case_name": "current"}}
	payload := promptEvaluationPayloadWithCases(map[string]any{
		"custom": "preserved",
	}, cases)
	if payload["custom"] != "preserved" || len(promptEvaluationCases(payload)) != 1 {
		t.Fatalf("current payload fields were not preserved: %#v", payload)
	}
}

func TestPromptEvaluationCaseProjectionsPopulateCurrentFields(t *testing.T) {
	assertCanonical := func(t *testing.T, item map[string]any) {
		t.Helper()
		for _, key := range []string{"case_name", "variables", "expected_contains", "tags"} {
			if _, exists := item[key]; !exists {
				t.Fatalf("canonical field %q missing: %#v", key, item)
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

func TestPromptEvaluationCaseNormalizerUsesCanonicalFields(t *testing.T) {
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
	}
	refs := promptEvaluationExplicitDatasetVersionRefs(payload)
	if len(refs) != 1 || refs[0].DatasetVersionID != "version-1" || refs[0].DatasetAssetID != "dataset-2" || refs[0].DatasetName != "Current Dataset" {
		t.Fatalf("canonical refs = %#v", refs)
	}
	ids := promptEvaluationLinkedDatasetIDs(payload)
	if len(ids) != 2 || ids[0] != "dataset-1" || ids[1] != "dataset-2" {
		t.Fatalf("canonical ids = %#v", ids)
	}
}

func TestPromptEvaluationMetricsAndExperimentDimensionsHaveDistinctContracts(t *testing.T) {
	payload := map[string]any{
		"metric_contract":       []any{"case_count", "pass_rate"},
		"metric_notes":          []any{"按当前快照统计"},
		"linked_dataset_ids":    []any{"dataset-1", "dataset-2"},
		"experiment_dimensions": []any{"命中率", map[string]any{"name": "中文一致性", "weight": 2}},
	}
	dimensions := promptEvaluationExperimentDimensions(payload)
	if len(dimensions) != 2 || dimensions[0].Name != "命中率" || dimensions[1].Name != "中文一致性" {
		t.Fatalf("canonical experiment dimensions = %#v", dimensions)
	}
	profile := promptEvaluationAssetProfileFromPayload(mustJSONBytes(payload), pgtype.UUID{Bytes: [16]byte{1}, Valid: true})
	if profile.EvaluationDimensionCount != 2 || profile.ExperimentDimensionCount != 2 || profile.LinkedDatasetCount != 2 || profile.LinkedPromptCount != 1 {
		t.Fatalf("profile = %#v", profile)
	}
}

func TestPromptEvaluationExperimentContextUsesCanonicalFieldsOnly(t *testing.T) {
	payload := map[string]any{
		"experiment_target":     "current target",
		"baseline_output":       "current baseline",
		"experiment_dimensions": []any{"命中率"},
	}
	dimensions := promptEvaluationExperimentDimensions(payload)
	if len(dimensions) != 1 || dimensions[0].ExperimentTarget != "current target" || dimensions[0].BaselineOutput != "current baseline" {
		t.Fatalf("canonical context = %#v", dimensions)
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

func TestPromptEvaluationAssetTypesRejectInvalidValues(t *testing.T) {
	if !validPromptEvaluationAssetType(promptEvaluationAssetDataset) || !validPromptEvaluationAssetType(promptEvaluationAssetTestSuite) {
		t.Fatal("current asset types were rejected")
	}
	for _, invalid := range []string{"", "unknown"} {
		if validPromptEvaluationAssetType(invalid) {
			t.Fatalf("invalid asset type %q was accepted", invalid)
		}
	}
}
