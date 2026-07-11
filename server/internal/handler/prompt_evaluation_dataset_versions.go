package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) CreatePromptEvaluationDatasetFromTraces(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	if asset.AssetType != promptEvaluationAssetDataset {
		writeError(w, http.StatusBadRequest, "only 数据集 assets can import trace events")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CreatePromptEvaluationDatasetFromTracesRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid trace dataset import payload")
			return
		}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	traceEvents, ok := h.promptEvaluationTraceEventsForDataset(w, r, asset.WorkspaceID, req, limit)
	if !ok {
		return
	}
	if len(traceEvents) == 0 {
		writeError(w, http.StatusBadRequest, "no trace events found for dataset import")
		return
	}
	existing, err := h.Queries.ListPromptEvaluationCases(r.Context(), db.ListPromptEvaluationCasesParams{WorkspaceID: asset.WorkspaceID, AssetID: asset.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to allocate trace dataset case indexes")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start trace dataset import transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	cases := make([]PromptEvaluationCaseResponse, 0, len(traceEvents))
	traceResp := make([]TaskTraceEventResponse, 0, len(traceEvents))
	for index, event := range traceEvents {
		caseIndex := int32(len(existing) + index)
		expectedContains := promptEvaluationTraceExpectedContains(event, req.ExpectedContains)
		created, err := qtx.CreatePromptEvaluationCase(r.Context(), db.CreatePromptEvaluationCaseParams{
			WorkspaceID:      asset.WorkspaceID,
			AssetID:          asset.ID,
			PromptID:         asset.PromptID,
			CaseIndex:        caseIndex,
			CaseName:         promptEvaluationTraceCaseName(event, caseIndex),
			Variables:        mustJSONBytes(promptEvaluationTraceVariables(event)),
			ExpectedContains: mustJSONBytes(expectedContains),
			Input:            mustJSONBytes(promptEvaluationTraceInput(event)),
			Expected:         mustJSONBytes(promptEvaluationTraceExpected(event, expectedContains)),
			Tags:             mustJSONBytes(promptEvaluationTraceTags(event, req.Tags)),
			Status:           "启用",
			Source:           "trace",
			CreatedBy:        parseUUID(userID),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create trace dataset case")
			return
		}
		assertions, err := syncPromptEvaluationCaseAssertions(r.Context(), qtx, created, mustJSONBytes(expectedContains))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create trace dataset assertions")
			return
		}
		if err := syncPromptEvaluationDatasetRow(r.Context(), qtx, asset, created); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to sync trace dataset row")
			return
		}
		cases = append(cases, promptEvaluationCaseToResponse(created, assertions))
		traceResp = append(traceResp, taskTraceEventToResponse(event))
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit trace dataset import")
		return
	}
	updatedAsset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{ID: asset.ID, WorkspaceID: asset.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload trace dataset asset")
		return
	}
	writeJSON(w, http.StatusCreated, PromptEvaluationDatasetFromTracesResponse{
		Asset:        promptEvaluationAssetToResponse(updatedAsset),
		Cases:        cases,
		TraceEvents:  traceResp,
		CreatedCount: len(cases),
		SkippedCount: 0,
		Source:       "trace",
	})
}

func (h *Handler) ListPromptEvaluationDatasetVersions(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	if asset.AssetType != promptEvaluationAssetDataset {
		writeError(w, http.StatusBadRequest, "only 数据集 assets have versions")
		return
	}
	limit := int32(20)
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if parsed > 100 {
			parsed = 100
		}
		limit = int32(parsed)
	}
	items, err := h.Queries.ListPromptEvaluationDatasetVersions(r.Context(), db.ListPromptEvaluationDatasetVersionsParams{
		WorkspaceID:    asset.WorkspaceID,
		DatasetAssetID: asset.ID,
		Limit:          limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list dataset versions")
		return
	}
	resp := make([]PromptEvaluationDatasetVersionResponse, len(items))
	for i, item := range items {
		resp[i] = promptEvaluationDatasetVersionToResponse(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) ListPromptEvaluationDatasetVersionTagTrends(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	if asset.AssetType != promptEvaluationAssetDataset {
		writeError(w, http.StatusBadRequest, "only 数据集 assets have version tag trends")
		return
	}
	limit := pgtype.Int4{Int32: 200, Valid: true}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 500")
			return
		}
		limit = pgtype.Int4{Int32: int32(parsed), Valid: true}
	}
	versionLimit := pgtype.Int4{Int32: 20, Valid: true}
	if raw := strings.TrimSpace(r.URL.Query().Get("version_limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "version_limit must be between 1 and 100")
			return
		}
		versionLimit = pgtype.Int4{Int32: int32(parsed), Valid: true}
	}
	rows, err := h.Queries.ListPromptEvaluationDatasetVersionTagTrends(r.Context(), db.ListPromptEvaluationDatasetVersionTagTrendsParams{
		WorkspaceID:    asset.WorkspaceID,
		DatasetAssetID: asset.ID,
		Limit:          limit,
		VersionLimit:   versionLimit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list dataset version tag trends")
		return
	}
	resp := make([]PromptEvaluationDatasetVersionTagTrendResponse, 0, len(rows))
	for _, row := range rows {
		if !row.Tag.Valid || strings.TrimSpace(row.Tag.String) == "" {
			continue
		}
		resp = append(resp, PromptEvaluationDatasetVersionTagTrendResponse{
			DatasetVersionID: uuidToString(row.DatasetVersionID),
			Version:          row.Version,
			VersionLabel:     row.VersionLabel,
			CreatedAt:        timestampToString(row.CreatedAt),
			Tag:              row.Tag.String,
			CaseCount:        row.CaseCount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) CreatePromptEvaluationDatasetVersion(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	if asset.AssetType != promptEvaluationAssetDataset {
		writeError(w, http.StatusBadRequest, "only 数据集 assets can create versions")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CreatePromptEvaluationDatasetVersionRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid dataset version payload")
			return
		}
	}
	metadata, ok := jsonObjectField(w, req.Metadata, "metadata")
	if !ok {
		return
	}
	if metadata == nil {
		metadata = mustJSONBytes(map[string]any{})
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start dataset version transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	version, err := h.createPromptEvaluationDatasetVersionFromCurrent(r.Context(), qtx, asset, parseUUID(userID), strings.TrimSpace(req.VersionLabel), metadata)
	if errors.Is(err, errPromptEvaluationDatasetVersionNoRows) {
		writeError(w, http.StatusBadRequest, "dataset version requires at least one enabled row")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create dataset version")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit dataset version")
		return
	}
	writeJSON(w, http.StatusCreated, promptEvaluationDatasetVersionToResponse(version))
}

func (h *Handler) createPromptEvaluationDatasetVersionFromCurrent(ctx context.Context, qtx *db.Queries, asset db.PromptEvaluationAsset, createdBy pgtype.UUID, versionLabel string, metadata []byte) (db.PromptEvaluationDatasetVersion, error) {
	rows, err := qtx.ListPromptEvaluationDatasetRows(ctx, db.ListPromptEvaluationDatasetRowsParams{
		WorkspaceID:    asset.WorkspaceID,
		DatasetAssetID: asset.ID,
		Status:         pgtype.Text{String: "启用", Valid: true},
	})
	if err != nil {
		return db.PromptEvaluationDatasetVersion{}, err
	}
	if len(rows) == 0 {
		return db.PromptEvaluationDatasetVersion{}, errPromptEvaluationDatasetVersionNoRows
	}
	nextVersion, err := qtx.NextPromptEvaluationDatasetVersion(ctx, db.NextPromptEvaluationDatasetVersionParams{
		WorkspaceID:    asset.WorkspaceID,
		DatasetAssetID: asset.ID,
	})
	if err != nil {
		return db.PromptEvaluationDatasetVersion{}, err
	}
	version, err := qtx.CreatePromptEvaluationDatasetVersion(ctx, db.CreatePromptEvaluationDatasetVersionParams{
		WorkspaceID:    asset.WorkspaceID,
		DatasetAssetID: asset.ID,
		Version:        nextVersion,
		RowCount:       int32(len(rows)),
		RowFingerprint: promptEvaluationDatasetRowsFingerprint(rows),
		VersionLabel:   strings.TrimSpace(versionLabel),
		Metadata:       metadata,
		CreatedBy:      createdBy,
	})
	if err != nil {
		return db.PromptEvaluationDatasetVersion{}, err
	}
	if err := qtx.CreatePromptEvaluationDatasetVersionRowsFromCurrent(ctx, db.CreatePromptEvaluationDatasetVersionRowsFromCurrentParams{
		WorkspaceID:      asset.WorkspaceID,
		DatasetAssetID:   asset.ID,
		DatasetVersionID: version.ID,
	}); err != nil {
		return db.PromptEvaluationDatasetVersion{}, err
	}
	payload := decodePayloadObject(asset.Payload)
	payload["最近数据集版本"] = promptEvaluationDatasetVersionSummary(version)
	payload["数据集版本说明"] = "数据集版本是当前启用样本行的不可变快照；评估运行会在证据中记录当次绑定版本，保证后续复盘可追溯。"
	if _, err := qtx.UpdatePromptEvaluationAsset(ctx, db.UpdatePromptEvaluationAssetParams{
		ID:          asset.ID,
		WorkspaceID: asset.WorkspaceID,
		PromptID:    asset.PromptID,
		Payload:     mustJSONBytes(payload),
	}); err != nil {
		return db.PromptEvaluationDatasetVersion{}, err
	}
	return version, nil
}

func (h *Handler) ListPromptEvaluationDatasetVersionRows(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	if asset.AssetType != promptEvaluationAssetDataset {
		writeError(w, http.StatusBadRequest, "only 数据集 assets have version rows")
		return
	}
	versionID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "versionId"), "dataset version id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListPromptEvaluationDatasetVersionRows(r.Context(), db.ListPromptEvaluationDatasetVersionRowsParams{
		WorkspaceID:      asset.WorkspaceID,
		DatasetVersionID: versionID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list dataset version rows")
		return
	}
	resp := make([]PromptEvaluationDatasetVersionRowResponse, len(rows))
	for i, row := range rows {
		if row.DatasetAssetID != asset.ID {
			writeError(w, http.StatusNotFound, "dataset version does not belong to this asset")
			return
		}
		resp[i] = promptEvaluationDatasetVersionRowToResponse(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) DiffPromptEvaluationDatasetVersion(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	if asset.AssetType != promptEvaluationAssetDataset {
		writeError(w, http.StatusBadRequest, "only 数据集 assets have version diff")
		return
	}
	baseVersionID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "versionId"), "dataset version id")
	if !ok {
		return
	}
	targetRaw := strings.TrimSpace(r.URL.Query().Get("target_version_id"))
	if targetRaw == "" {
		writeError(w, http.StatusBadRequest, "target_version_id is required")
		return
	}
	targetVersionID, ok := parseUUIDOrBadRequest(w, targetRaw, "target dataset version id")
	if !ok {
		return
	}
	baseVersion, err := h.Queries.GetPromptEvaluationDatasetVersionInAsset(r.Context(), db.GetPromptEvaluationDatasetVersionInAssetParams{
		WorkspaceID:    asset.WorkspaceID,
		DatasetAssetID: asset.ID,
		ID:             baseVersionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "base dataset version not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load base dataset version")
		return
	}
	targetVersion, err := h.Queries.GetPromptEvaluationDatasetVersionInAsset(r.Context(), db.GetPromptEvaluationDatasetVersionInAssetParams{
		WorkspaceID:    asset.WorkspaceID,
		DatasetAssetID: asset.ID,
		ID:             targetVersionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "target dataset version not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load target dataset version")
		return
	}
	baseRows, err := h.Queries.ListPromptEvaluationDatasetVersionRows(r.Context(), db.ListPromptEvaluationDatasetVersionRowsParams{
		WorkspaceID:      asset.WorkspaceID,
		DatasetVersionID: baseVersion.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list base dataset version rows")
		return
	}
	targetRows, err := h.Queries.ListPromptEvaluationDatasetVersionRows(r.Context(), db.ListPromptEvaluationDatasetVersionRowsParams{
		WorkspaceID:      asset.WorkspaceID,
		DatasetVersionID: targetVersion.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list target dataset version rows")
		return
	}
	diff := buildPromptEvaluationDatasetVersionDiff(baseRows, targetRows)
	diff.BaseVersion = promptEvaluationDatasetVersionToResponse(baseVersion)
	diff.TargetVersion = promptEvaluationDatasetVersionToResponse(targetVersion)
	writeJSON(w, http.StatusOK, diff)
}

func (h *Handler) RestorePromptEvaluationDatasetVersion(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	if asset.AssetType != promptEvaluationAssetDataset {
		writeError(w, http.StatusBadRequest, "only 数据集 assets can restore versions")
		return
	}
	versionID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "versionId"), "dataset version id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req RestorePromptEvaluationDatasetVersionRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid dataset version restore payload")
			return
		}
	}
	requestMetadata, ok := jsonObjectField(w, req.Metadata, "metadata")
	if !ok {
		return
	}
	version, err := h.Queries.GetPromptEvaluationDatasetVersionInAsset(r.Context(), db.GetPromptEvaluationDatasetVersionInAssetParams{
		WorkspaceID:    asset.WorkspaceID,
		DatasetAssetID: asset.ID,
		ID:             versionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "dataset version not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load dataset version")
		return
	}
	rows, err := h.Queries.ListPromptEvaluationDatasetVersionRows(r.Context(), db.ListPromptEvaluationDatasetVersionRowsParams{
		WorkspaceID:      asset.WorkspaceID,
		DatasetVersionID: version.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list dataset version rows")
		return
	}
	if len(rows) == 0 {
		writeError(w, http.StatusBadRequest, "dataset version has no rows to restore")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start dataset version restore transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	if err := qtx.DeletePromptEvaluationCasesByAsset(r.Context(), db.DeletePromptEvaluationCasesByAssetParams{
		WorkspaceID: asset.WorkspaceID,
		AssetID:     asset.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear current dataset cases")
		return
	}

	restoredCases := make([]PromptEvaluationCaseResponse, 0, len(rows))
	for _, row := range rows {
		created, err := qtx.CreatePromptEvaluationCase(r.Context(), db.CreatePromptEvaluationCaseParams{
			WorkspaceID:      asset.WorkspaceID,
			AssetID:          asset.ID,
			PromptID:         asset.PromptID,
			CaseIndex:        row.RowIndex,
			CaseName:         row.RowName,
			Variables:        row.Variables,
			ExpectedContains: row.ExpectedContains,
			Input:            []byte("{}"),
			Expected:         row.Expected,
			Tags:             row.Tags,
			Status:           "启用",
			Source:           "manual",
			CreatedBy:        parseUUID(userID),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to recreate dataset case from version")
			return
		}
		assertions, err := syncPromptEvaluationCaseAssertions(r.Context(), qtx, created, row.ExpectedContains)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to recreate dataset case assertions")
			return
		}
		if err := syncPromptEvaluationDatasetRow(r.Context(), qtx, asset, created); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to sync restored dataset row")
			return
		}
		restoredCases = append(restoredCases, promptEvaluationCaseToResponse(created, assertions))
	}

	payload := normalizePromptEvaluationPayloadObject(promptEvaluationPayloadWithCases(decodePayloadObject(asset.Payload), promptEvaluationPayloadCasesFromDatasetVersionRows(rows)))
	payload["最近恢复数据集版本"] = map[string]any{
		"dataset_version_id": uuidToString(version.ID),
		"version":            version.Version,
		"version_label":      version.VersionLabel,
		"restored_at":        time.Now().Format(time.RFC3339),
	}
	payloadBytes := mustJSONBytes(payload)
	profile := promptEvaluationAssetProfileFromPayload(payloadBytes, asset.PromptID, asset.AssetType)
	updatedAsset, err := qtx.UpdatePromptEvaluationAsset(r.Context(), db.UpdatePromptEvaluationAssetParams{
		ID:                       asset.ID,
		WorkspaceID:              asset.WorkspaceID,
		PromptID:                 asset.PromptID,
		Payload:                  payloadBytes,
		StructureSchema:          pgtype.Text{String: profile.StructureSchema, Valid: true},
		StructuredCaseCount:      pgtype.Int4{Int32: profile.StructuredCaseCount, Valid: true},
		StructuredVariableCount:  pgtype.Int4{Int32: profile.StructuredVariableCount, Valid: true},
		StructuredAssertionCount: pgtype.Int4{Int32: profile.StructuredAssertionCount, Valid: true},
		LinkedDatasetCount:       pgtype.Int4{Int32: profile.LinkedDatasetCount, Valid: true},
		LinkedPromptCount:        pgtype.Int4{Int32: profile.LinkedPromptCount, Valid: true},
		EvaluationDimensionCount: pgtype.Int4{Int32: profile.EvaluationDimensionCount, Valid: true},
		ExperimentDimensionCount: pgtype.Int4{Int32: profile.ExperimentDimensionCount, Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update restored dataset asset")
		return
	}
	metadata := promptEvaluationDatasetVersionRestoreMetadata(version, requestMetadata)
	versionLabel := strings.TrimSpace(req.VersionLabel)
	if versionLabel == "" {
		versionLabel = fmt.Sprintf("从 v%d 恢复", version.Version)
	}
	restoredVersion, err := h.createPromptEvaluationDatasetVersionFromCurrent(r.Context(), qtx, updatedAsset, parseUUID(userID), versionLabel, metadata)
	if errors.Is(err, errPromptEvaluationDatasetVersionNoRows) {
		writeError(w, http.StatusBadRequest, "dataset version requires at least one enabled row")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create restored dataset version")
		return
	}
	finalAsset, err := qtx.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{
		ID:          asset.ID,
		WorkspaceID: asset.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload restored dataset asset")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit dataset version restore")
		return
	}
	writeJSON(w, http.StatusOK, RestorePromptEvaluationDatasetVersionResponse{
		Asset:           promptEvaluationAssetToResponse(finalAsset),
		RestoredFrom:    promptEvaluationDatasetVersionToResponse(version),
		RestoredVersion: promptEvaluationDatasetVersionToResponse(restoredVersion),
		RestoredCases:   restoredCases,
	})
}

func (h *Handler) promptEvaluationTraceEventsForDataset(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, req CreatePromptEvaluationDatasetFromTracesRequest, limit int32) ([]db.TaskTraceEvent, bool) {
	if len(req.TaskIDs) > 0 {
		events := make([]db.TaskTraceEvent, 0, limit)
		taskIDs, ok := parseUUIDSliceOrBadRequest(w, req.TaskIDs, "task_ids")
		if !ok {
			return nil, false
		}
		for _, taskID := range taskIDs {
			items, err := h.Queries.ListTaskTraceEventsByTask(r.Context(), taskID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to list task trace events")
				return nil, false
			}
			for _, item := range items {
				if item.WorkspaceID != workspaceID {
					continue
				}
				if req.EventType != "" && item.EventType != req.EventType {
					continue
				}
				events = append(events, item)
				if int32(len(events)) >= limit {
					return events, true
				}
			}
		}
		return events, true
	}
	var eventType pgtype.Text
	if strings.TrimSpace(req.EventType) != "" {
		eventType = pgtype.Text{String: strings.TrimSpace(req.EventType), Valid: true}
	}
	events, err := h.Queries.ListWorkspaceTaskTraceEvents(r.Context(), db.ListWorkspaceTaskTraceEventsParams{
		WorkspaceID: workspaceID,
		Limit:       limit,
		Offset:      0,
		EventType:   eventType,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspace trace events")
		return nil, false
	}
	return events, true
}

func promptEvaluationTraceCaseName(event db.TaskTraceEvent, caseIndex int32) string {
	name := strings.TrimSpace(event.EventName)
	if name == "" {
		name = strings.TrimSpace(event.EventType)
	}
	if name == "" {
		name = "trace 事件"
	}
	return "trace样本 " + strconv.Itoa(int(caseIndex+1)) + "：" + name
}

func promptEvaluationTraceExpectedContains(event db.TaskTraceEvent, requested []string) []string {
	values := make([]string, 0, len(requested)+2)
	for _, item := range requested {
		if text := strings.TrimSpace(item); text != "" {
			values = append(values, text)
		}
	}
	if strings.TrimSpace(event.Status) != "" {
		values = append(values, event.Status)
	}
	if strings.TrimSpace(event.FailureReason) != "" {
		values = append(values, event.FailureReason)
	}
	return values
}

func promptEvaluationTraceTags(event db.TaskTraceEvent, requested []string) []string {
	tags := []string{"trace导入", event.EventType, event.Status}
	for _, item := range requested {
		if text := strings.TrimSpace(item); text != "" {
			tags = append(tags, text)
		}
	}
	return compactStrings(tags)
}

func promptEvaluationTraceVariables(event db.TaskTraceEvent) map[string]any {
	return map[string]any{
		"task_id":        uuidToString(event.TaskID),
		"trace_event_id": uuidToString(event.ID),
		"event_type":     event.EventType,
		"event_name":     event.EventName,
		"status":         event.Status,
		"provider":       event.Provider,
		"model":          event.Model,
	}
}

func promptEvaluationTraceInput(event db.TaskTraceEvent) map[string]any {
	return map[string]any{
		"来源":        "task_trace_event",
		"任务ID":      uuidToString(event.TaskID),
		"trace事件ID": uuidToString(event.ID),
		"事件类型":      event.EventType,
		"事件名称":      event.EventName,
		"状态":        event.Status,
		"耗时毫秒":      int8ToPtr(event.DurationMs),
		"总耗时毫秒":     int8ToPtr(event.TotalMs),
		"provider":  event.Provider,
		"model":     event.Model,
		"输入token":   event.InputTokens,
		"输出token":   event.OutputTokens,
		"失败原因":      event.FailureReason,
		"错误类型":      event.ErrorType,
		"metadata":  decodePayloadObject(event.Metadata),
	}
}

func promptEvaluationTraceExpected(event db.TaskTraceEvent, expectedContains []string) map[string]any {
	return map[string]any{
		"期望包含": expectedContains,
		"来源任务": uuidToString(event.TaskID),
		"状态":   event.Status,
	}
}
