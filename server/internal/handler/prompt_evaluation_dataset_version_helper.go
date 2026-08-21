package handler

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/prompteval"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func promptEvaluationDatasetRowsFingerprint(rows []db.PromptEvaluationDatasetRow) string {
	snapshot := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		snapshot = append(snapshot, map[string]any{
			"row_index":         row.RowIndex,
			"row_name":          row.RowName,
			"variables":         decodeJSONDefault(row.Variables, map[string]any{}),
			"expected_contains": decodeJSONDefault(row.ExpectedContains, []any{}),
			"expected":          decodeJSONDefault(row.Expected, map[string]any{}),
			"tags":              decodeJSONDefault(row.Tags, []any{}),
			"source":            row.Source,
		})
	}
	sum := sha256.Sum256(prompteval.MustJSONBytes(snapshot))
	return fmt.Sprintf("%x", sum[:])
}

func promptEvaluationDatasetVersionRowFingerprint(row db.PromptEvaluationDatasetVersionRow) string {
	snapshot := map[string]any{
		"row_index":         row.RowIndex,
		"row_name":          row.RowName,
		"variables":         decodeJSONDefault(row.Variables, map[string]any{}),
		"expected_contains": decodeJSONDefault(row.ExpectedContains, []any{}),
		"expected":          decodeJSONDefault(row.Expected, map[string]any{}),
		"tags":              decodeJSONDefault(row.Tags, []any{}),
		"source":            row.Source,
	}
	sum := sha256.Sum256(prompteval.MustJSONBytes(snapshot))
	return fmt.Sprintf("%x", sum[:])
}

func buildPromptEvaluationDatasetVersionDiff(baseRows []db.PromptEvaluationDatasetVersionRow, targetRows []db.PromptEvaluationDatasetVersionRow) PromptEvaluationDatasetVersionDiffResponse {
	baseByIndex := make(map[int32]db.PromptEvaluationDatasetVersionRow, len(baseRows))
	targetByIndex := make(map[int32]db.PromptEvaluationDatasetVersionRow, len(targetRows))
	indexSet := map[int32]bool{}
	for _, row := range baseRows {
		baseByIndex[row.RowIndex] = row
		indexSet[row.RowIndex] = true
	}
	for _, row := range targetRows {
		targetByIndex[row.RowIndex] = row
		indexSet[row.RowIndex] = true
	}
	indexes := make([]int, 0, len(indexSet))
	for index := range indexSet {
		indexes = append(indexes, int(index))
	}
	sort.Ints(indexes)

	resp := PromptEvaluationDatasetVersionDiffResponse{
		Summary: map[string]int{
			"新增":  0,
			"删除":  0,
			"变更":  0,
			"未变更": 0,
		},
		Added:     []PromptEvaluationDatasetVersionRowResponse{},
		Removed:   []PromptEvaluationDatasetVersionRowResponse{},
		Changed:   []PromptEvaluationDatasetVersionChangedRow{},
		Unchanged: []PromptEvaluationDatasetVersionRowResponse{},
	}
	for _, rawIndex := range indexes {
		index := int32(rawIndex)
		base, hasBase := baseByIndex[index]
		target, hasTarget := targetByIndex[index]
		switch {
		case !hasBase && hasTarget:
			resp.Added = append(resp.Added, promptEvaluationDatasetVersionRowToResponse(target))
			resp.Summary["新增"]++
		case hasBase && !hasTarget:
			resp.Removed = append(resp.Removed, promptEvaluationDatasetVersionRowToResponse(base))
			resp.Summary["删除"]++
		case promptEvaluationDatasetVersionRowFingerprint(base) != promptEvaluationDatasetVersionRowFingerprint(target):
			resp.Changed = append(resp.Changed, PromptEvaluationDatasetVersionChangedRow{
				RowIndex: index,
				Base:     promptEvaluationDatasetVersionRowToResponse(base),
				Target:   promptEvaluationDatasetVersionRowToResponse(target),
			})
			resp.Summary["变更"]++
		default:
			resp.Unchanged = append(resp.Unchanged, promptEvaluationDatasetVersionRowToResponse(target))
			resp.Summary["未变更"]++
		}
	}
	return resp
}

func promptEvaluationPayloadCasesFromDatasetVersionRows(rows []db.PromptEvaluationDatasetVersionRow) []map[string]any {
	cases := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		cases = append(cases, map[string]any{
			"name":              row.RowName,
			"case_name":         row.RowName,
			"名称":                row.RowName,
			"variables":         decodeJSONDefault(row.Variables, map[string]any{}),
			"变量":                decodeJSONDefault(row.Variables, map[string]any{}),
			"expected_contains": decodeJSONDefault(row.ExpectedContains, []any{}),
			"期望包含":              decodeJSONDefault(row.ExpectedContains, []any{}),
			"expected":          decodeJSONDefault(row.Expected, map[string]any{}),
			"期望":                decodeJSONDefault(row.Expected, map[string]any{}),
			"tags":              decodeJSONDefault(row.Tags, []any{}),
			"标签":                decodeJSONDefault(row.Tags, []any{}),
		})
	}
	return cases
}

func promptEvaluationDatasetVersionRestoreMetadata(version db.PromptEvaluationDatasetVersion, requestMetadata []byte) []byte {
	metadata := map[string]any{
		"来源":        "数据集版本恢复",
		"恢复来源版本":    version.Version,
		"恢复来源版本标识":  uuidToString(version.ID),
		"恢复来源版本名称":  version.VersionLabel,
		"恢复来源版本行指纹": version.RowFingerprint,
		"恢复时间":      time.Now().Format(time.RFC3339),
	}
	if len(requestMetadata) > 0 {
		var extra map[string]any
		if err := json.Unmarshal(requestMetadata, &extra); err == nil {
			for key, value := range extra {
				metadata[key] = value
			}
		}
	}
	return prompteval.MustJSONBytes(metadata)
}

func promptEvaluationDatasetVersionSummary(version db.PromptEvaluationDatasetVersion) map[string]any {
	return map[string]any{
		"dataset_version_id": uuidToString(version.ID),
		"version":            version.Version,
		"version_label":      version.VersionLabel,
		"row_count":          version.RowCount,
		"row_fingerprint":    version.RowFingerprint,
		"created_at":         timestampToString(version.CreatedAt),
	}
}

func (h *Handler) promptEvaluationDatasetVersionBindings(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, payload map[string]any) ([]map[string]any, bool) {
	datasetIDs := promptEvaluationLinkedDatasetIDs(payload)
	explicit := promptEvaluationExplicitDatasetVersionRefs(payload)
	if len(datasetIDs) == 0 && len(explicit) == 0 {
		return nil, true
	}
	bindings := make([]map[string]any, 0, len(datasetIDs)+len(explicit))
	seenVersions := map[string]bool{}
	explicitDatasets := map[string]bool{}
	for _, ref := range explicit {
		datasetID, err := util.ParseUUID(ref.DatasetAssetID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "linked dataset version has invalid dataset_asset_id")
			return nil, false
		}
		versionID, err := util.ParseUUID(ref.DatasetVersionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "linked dataset version has invalid dataset_version_id")
			return nil, false
		}
		datasetKey := uuidToString(datasetID)
		versionKey := uuidToString(versionID)
		explicitDatasets[datasetKey] = true
		if seenVersions[datasetKey+"."+versionKey] {
			continue
		}
		version, err := h.Queries.GetPromptEvaluationDatasetVersionInAsset(r.Context(), db.GetPromptEvaluationDatasetVersionInAssetParams{
			WorkspaceID:    workspaceID,
			DatasetAssetID: datasetID,
			ID:             versionID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "linked dataset version does not belong to this workspace dataset")
				return nil, false
			}
			writeError(w, http.StatusInternalServerError, "failed to load linked dataset version")
			return nil, false
		}
		summary := promptEvaluationDatasetVersionSummary(version)
		summary["dataset_asset_id"] = datasetKey
		summary["绑定方式"] = "资产声明的明确数据集版本"
		if strings.TrimSpace(ref.DatasetName) != "" {
			summary["dataset_name"] = strings.TrimSpace(ref.DatasetName)
		}
		bindings = append(bindings, summary)
		seenVersions[datasetKey+"."+versionKey] = true
	}
	seen := map[string]bool{}
	for _, rawID := range datasetIDs {
		datasetID, err := util.ParseUUID(rawID)
		if err != nil {
			continue
		}
		key := uuidToString(datasetID)
		if key == "" || seen[key] || explicitDatasets[key] {
			continue
		}
		seen[key] = true
		version, err := h.Queries.GetLatestPromptEvaluationDatasetVersion(r.Context(), db.GetLatestPromptEvaluationDatasetVersionParams{
			WorkspaceID:    workspaceID,
			DatasetAssetID: datasetID,
		})
		if err != nil {
			continue
		}
		summary := promptEvaluationDatasetVersionSummary(version)
		summary["dataset_asset_id"] = key
		summary["绑定方式"] = "运行开始时读取最新数据集版本"
		bindings = append(bindings, summary)
	}
	return bindings, true
}

type promptEvaluationDatasetVersionRef struct {
	DatasetAssetID   string
	DatasetVersionID string
	DatasetName      string
}

func promptEvaluationExplicitDatasetVersionRefs(payload map[string]any) []promptEvaluationDatasetVersionRef {
	raw := firstValue(payload, "linked_dataset_versions", "数据集版本", "关联数据集版本")
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]promptEvaluationDatasetVersionRef, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		versionID := strings.TrimSpace(prompteval.StringFromAny(firstValue(m, "dataset_version_id", "version_id", "数据集版本ID")))
		if versionID == "" {
			continue
		}
		datasetID := strings.TrimSpace(prompteval.StringFromAny(firstValue(m, "dataset_id", "dataset_asset_id", "数据集ID")))
		if datasetID == "" {
			result = append(result, promptEvaluationDatasetVersionRef{DatasetVersionID: versionID})
			continue
		}
		result = append(result, promptEvaluationDatasetVersionRef{
			DatasetAssetID:   datasetID,
			DatasetVersionID: versionID,
			DatasetName:      strings.TrimSpace(prompteval.StringFromAny(firstValue(m, "dataset_name", "数据集名称", "name", "名称"))),
		})
	}
	return result
}

func promptEvaluationLinkedDatasetIDs(payload map[string]any) []string {
	values := []any{
		firstValue(payload, "linked_dataset_ids", "dataset_ids", "数据集ID", "关联数据集ID"),
	}
	if nested, ok := firstValue(payload, "linked_dataset_versions", "数据集版本", "关联数据集版本").([]any); ok {
		for _, item := range nested {
			if m, ok := item.(map[string]any); ok {
				values = append(values, firstValue(m, "dataset_id", "dataset_asset_id", "数据集ID"))
			}
		}
	}
	result := []string{}
	for _, value := range values {
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				result = append(result, strings.TrimSpace(v))
			}
		case []any:
			for _, item := range v {
				if s := strings.TrimSpace(prompteval.StringFromAny(item)); s != "" {
					result = append(result, s)
				}
			}
		case []string:
			for _, item := range v {
				if strings.TrimSpace(item) != "" {
					result = append(result, strings.TrimSpace(item))
				}
			}
		}
	}
	return result
}
