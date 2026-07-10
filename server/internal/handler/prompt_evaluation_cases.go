package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) ListPromptEvaluationCases(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var assetID pgtype.UUID
	if value := r.URL.Query().Get("asset_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "asset_id")
		if !ok {
			return
		}
		assetID = parsed
	}
	var status pgtype.Text
	if value := r.URL.Query().Get("status"); value != "" {
		if !validPromptEvaluationCaseStatus(value) {
			writeError(w, http.StatusBadRequest, promptEvaluationCaseStatusError())
			return
		}
		status = pgtype.Text{String: value, Valid: true}
	}
	var source pgtype.Text
	if value := strings.TrimSpace(r.URL.Query().Get("source")); value != "" {
		if value != "manual" && value != "trace" && value != "payload" {
			writeError(w, http.StatusBadRequest, "source must be manual, trace, or payload")
			return
		}
		source = pgtype.Text{String: value, Valid: true}
	}
	var tag pgtype.Text
	if value := strings.TrimSpace(r.URL.Query().Get("tag")); value != "" {
		tag = pgtype.Text{String: value, Valid: true}
	}
	var keyword pgtype.Text
	if value := strings.TrimSpace(r.URL.Query().Get("keyword")); value != "" {
		keyword = pgtype.Text{String: value, Valid: true}
	}
	var limit pgtype.Int4
	effectiveLimit := int32(5000)
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 500")
			return
		}
		effectiveLimit = int32(parsed)
		limit = pgtype.Int4{Int32: effectiveLimit, Valid: true}
	}
	sortByValue := strings.TrimSpace(r.URL.Query().Get("sort_by"))
	if sortByValue == "" {
		sortByValue = "case_index"
	}
	if !validPromptEvaluationCaseSortBy(sortByValue) {
		writeError(w, http.StatusBadRequest, "sort_by must be case_index, case_name, source, created_at, or updated_at")
		return
	}
	sortDirectionValue := strings.TrimSpace(r.URL.Query().Get("sort_direction"))
	if sortDirectionValue == "" {
		sortDirectionValue = "asc"
	}
	if sortDirectionValue != "asc" && sortDirectionValue != "desc" {
		writeError(w, http.StatusBadRequest, "sort_direction must be asc or desc")
		return
	}
	offset := int32(0)
	var cursorID pgtype.UUID
	var cursorCaseIndex pgtype.Int4
	var cursorCaseName pgtype.Text
	var cursorSource pgtype.Text
	var cursorCreatedAt pgtype.Timestamptz
	var cursorUpdatedAt pgtype.Timestamptz
	if value := strings.TrimSpace(r.URL.Query().Get("cursor")); value != "" {
		decodedCursor, ok := decodePromptEvaluationCaseCursor(w, value)
		if !ok {
			return
		}
		if decodedCursor.SortBy != sortByValue || decodedCursor.SortDirection != sortDirectionValue {
			writeError(w, http.StatusBadRequest, "cursor does not match sort")
			return
		}
		parsedID, err := util.ParseUUID(decodedCursor.LastID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "cursor is invalid")
			return
		}
		offset = decodedCursor.Offset
		cursorID = parsedID
		switch sortByValue {
		case "case_index":
			cursorCaseIndex = pgtype.Int4{Int32: decodedCursor.CaseIndex, Valid: true}
		case "case_name":
			cursorCaseName = pgtype.Text{String: decodedCursor.CaseName, Valid: true}
		case "source":
			cursorSource = pgtype.Text{String: decodedCursor.Source, Valid: true}
		case "created_at":
			parsedTime, err := time.Parse(time.RFC3339Nano, decodedCursor.CreatedAt)
			if err != nil {
				writeError(w, http.StatusBadRequest, "cursor is invalid")
				return
			}
			cursorCreatedAt = pgtype.Timestamptz{Time: parsedTime, Valid: true}
		case "updated_at":
			parsedTime, err := time.Parse(time.RFC3339Nano, decodedCursor.UpdatedAt)
			if err != nil {
				writeError(w, http.StatusBadRequest, "cursor is invalid")
				return
			}
			cursorUpdatedAt = pgtype.Timestamptz{Time: parsedTime, Valid: true}
		}
	}
	totalCount, err := h.Queries.CountPromptEvaluationCases(r.Context(), db.CountPromptEvaluationCasesParams{
		WorkspaceID: workspaceUUID,
		AssetID:     assetID,
		Status:      status,
		Source:      source,
		Tag:         tag,
		Keyword:     keyword,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count prompt evaluation cases")
		return
	}
	cases, err := h.Queries.ListPromptEvaluationCases(r.Context(), db.ListPromptEvaluationCasesParams{
		WorkspaceID:     workspaceUUID,
		AssetID:         assetID,
		Status:          status,
		Source:          source,
		Tag:             tag,
		Keyword:         keyword,
		CursorID:        cursorID,
		Limit:           limit,
		SortBy:          pgtype.Text{String: sortByValue, Valid: true},
		SortDirection:   pgtype.Text{String: sortDirectionValue, Valid: true},
		CursorCaseIndex: cursorCaseIndex,
		CursorCaseName:  cursorCaseName,
		CursorSource:    cursorSource,
		CursorCreatedAt: cursorCreatedAt,
		CursorUpdatedAt: cursorUpdatedAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation cases")
		return
	}
	assertions, err := h.Queries.ListPromptEvaluationCaseAssertions(r.Context(), db.ListPromptEvaluationCaseAssertionsParams{
		WorkspaceID: workspaceUUID,
		AssetID:     assetID,
		Status:      status,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation case assertions")
		return
	}
	assertionsByCase := promptEvaluationAssertionsByCase(assertions)
	resp := make([]PromptEvaluationCaseResponse, len(cases))
	for i, item := range cases {
		resp[i] = promptEvaluationCaseToResponse(item, assertionsByCase[uuidToString(item.ID)])
	}
	nextOffset := offset + int32(len(resp))
	hasMore := int64(nextOffset) < totalCount
	var nextCursor *string
	if hasMore && len(cases) > 0 {
		cursor := encodePromptEvaluationCaseCursor(nextOffset, sortByValue, sortDirectionValue, cases[len(cases)-1])
		nextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":          resp,
		"total":          totalCount,
		"total_count":    totalCount,
		"limit":          effectiveLimit,
		"offset":         offset,
		"has_more":       hasMore,
		"next_cursor":    nextCursor,
		"sort_by":        sortByValue,
		"sort_direction": sortDirectionValue,
	})
}

func (h *Handler) ListPromptEvaluationCaseTagSummaries(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var assetID pgtype.UUID
	if value := r.URL.Query().Get("asset_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "asset_id")
		if !ok {
			return
		}
		assetID = parsed
	}
	var status pgtype.Text
	if value := r.URL.Query().Get("status"); value != "" {
		if !validPromptEvaluationCaseStatus(value) {
			writeError(w, http.StatusBadRequest, promptEvaluationCaseStatusError())
			return
		}
		status = pgtype.Text{String: value, Valid: true}
	}
	var source pgtype.Text
	if value := strings.TrimSpace(r.URL.Query().Get("source")); value != "" {
		if value != "manual" && value != "trace" && value != "payload" {
			writeError(w, http.StatusBadRequest, "source must be manual, trace, or payload")
			return
		}
		source = pgtype.Text{String: value, Valid: true}
	}
	var keyword pgtype.Text
	if value := strings.TrimSpace(r.URL.Query().Get("keyword")); value != "" {
		keyword = pgtype.Text{String: value, Valid: true}
	}
	limit := pgtype.Int4{Int32: 50, Valid: true}
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = pgtype.Int4{Int32: int32(parsed), Valid: true}
	}
	rows, err := h.Queries.ListPromptEvaluationCaseTagSummaries(r.Context(), db.ListPromptEvaluationCaseTagSummariesParams{
		WorkspaceID: workspaceUUID,
		AssetID:     assetID,
		Status:      status,
		Source:      source,
		Keyword:     keyword,
		Limit:       limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation case tag summaries")
		return
	}
	resp := make([]PromptEvaluationCaseTagSummaryResponse, 0, len(rows))
	for _, item := range rows {
		if !item.Tag.Valid || strings.TrimSpace(item.Tag.String) == "" {
			continue
		}
		resp = append(resp, PromptEvaluationCaseTagSummaryResponse{
			Tag:       item.Tag.String,
			CaseCount: item.CaseCount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) ListPromptEvaluationCaseTagDatasetSummaries(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var status pgtype.Text
	if value := r.URL.Query().Get("status"); value != "" {
		if !validPromptEvaluationCaseStatus(value) {
			writeError(w, http.StatusBadRequest, promptEvaluationCaseStatusError())
			return
		}
		status = pgtype.Text{String: value, Valid: true}
	}
	var source pgtype.Text
	if value := strings.TrimSpace(r.URL.Query().Get("source")); value != "" {
		if value != "manual" && value != "trace" && value != "payload" {
			writeError(w, http.StatusBadRequest, "source must be manual, trace, or payload")
			return
		}
		source = pgtype.Text{String: value, Valid: true}
	}
	var keyword pgtype.Text
	if value := strings.TrimSpace(r.URL.Query().Get("keyword")); value != "" {
		keyword = pgtype.Text{String: value, Valid: true}
	}
	limit := pgtype.Int4{Int32: 20, Valid: true}
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = pgtype.Int4{Int32: int32(parsed), Valid: true}
	}
	topDatasetLimit := pgtype.Int4{Int32: 3, Valid: true}
	if value := strings.TrimSpace(r.URL.Query().Get("top_dataset_limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 10 {
			writeError(w, http.StatusBadRequest, "top_dataset_limit must be between 1 and 10")
			return
		}
		topDatasetLimit = pgtype.Int4{Int32: int32(parsed), Valid: true}
	}
	rows, err := h.Queries.ListPromptEvaluationCaseTagDatasetSummaries(r.Context(), db.ListPromptEvaluationCaseTagDatasetSummariesParams{
		WorkspaceID:     workspaceUUID,
		Status:          status,
		Source:          source,
		Keyword:         keyword,
		Limit:           limit,
		TopDatasetLimit: topDatasetLimit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation case tag dataset summaries")
		return
	}
	byTag := make(map[string]*PromptEvaluationCaseTagDatasetSummaryResponse)
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		if !row.Tag.Valid || strings.TrimSpace(row.Tag.String) == "" {
			continue
		}
		tag := row.Tag.String
		item := byTag[tag]
		if item == nil {
			item = &PromptEvaluationCaseTagDatasetSummaryResponse{
				Tag:          tag,
				CaseCount:    row.TotalCaseCount,
				DatasetCount: row.DatasetCount,
				TopDatasets:  []PromptEvaluationCaseTagDatasetSummaryDatasetResponse{},
			}
			byTag[tag] = item
			order = append(order, tag)
		}
		item.TopDatasets = append(item.TopDatasets, PromptEvaluationCaseTagDatasetSummaryDatasetResponse{
			AssetID:   uuidToString(row.AssetID),
			AssetName: row.AssetName,
			CaseCount: row.CaseCount,
		})
	}
	resp := make([]PromptEvaluationCaseTagDatasetSummaryResponse, 0, len(order))
	for _, tag := range order {
		resp = append(resp, *byTag[tag])
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) ListPromptEvaluationCaseOperations(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	var limit pgtype.Int4
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = pgtype.Int4{Int32: int32(parsed), Valid: true}
	}
	rows, err := h.Queries.ListPromptEvaluationCaseOperations(r.Context(), db.ListPromptEvaluationCaseOperationsParams{
		WorkspaceID: asset.WorkspaceID,
		AssetID:     asset.ID,
		Limit:       limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation case operations")
		return
	}
	resp := make([]PromptEvaluationCaseOperationResponse, len(rows))
	for i, item := range rows {
		resp[i] = promptEvaluationCaseOperationToResponse(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) BulkUpdatePromptEvaluationCaseTags(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req BulkPromptEvaluationCaseTagsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid prompt evaluation bulk tag payload")
		return
	}
	assetID, ok := parseUUIDOrBadRequest(w, req.AssetID, "asset_id")
	if !ok {
		return
	}
	asset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{ID: assetID, WorkspaceID: workspaceUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation asset not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation asset")
		return
	}
	if asset.AssetType != promptEvaluationAssetDataset {
		writeError(w, http.StatusBadRequest, "only 数据集 assets support bulk case tag operations")
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode != "追加" && mode != "移除" && mode != "重命名" {
		writeError(w, http.StatusBadRequest, "mode must be 追加, 移除, or 重命名")
		return
	}
	targetTags := compactStrings(req.Tags)
	sourceTag := strings.TrimSpace(req.SourceTag)
	targetTag := strings.TrimSpace(req.TargetTag)
	if mode == "重命名" {
		if sourceTag == "" {
			writeError(w, http.StatusBadRequest, "source_tag is required when mode is 重命名")
			return
		}
		if targetTag == "" {
			writeError(w, http.StatusBadRequest, "target_tag is required when mode is 重命名")
			return
		}
		if sourceTag == targetTag {
			writeError(w, http.StatusBadRequest, "target_tag must be different from source_tag")
			return
		}
		targetTags = []string{targetTag}
	} else if len(targetTags) == 0 {
		writeError(w, http.StatusBadRequest, "tags must contain at least one non-empty tag")
		return
	}
	limit := req.Limit
	if limit == 0 {
		limit = 500
	}
	if limit < 1 || limit > 500 {
		writeError(w, http.StatusBadRequest, "limit must be between 1 and 500")
		return
	}
	var source pgtype.Text
	if value := strings.TrimSpace(req.Source); value != "" {
		if value != "manual" && value != "trace" && value != "payload" {
			writeError(w, http.StatusBadRequest, "source must be manual, trace, or payload")
			return
		}
		source = pgtype.Text{String: value, Valid: true}
	}
	var status pgtype.Text
	if value := strings.TrimSpace(req.Status); value != "" {
		if !validPromptEvaluationCaseStatus(value) {
			writeError(w, http.StatusBadRequest, promptEvaluationCaseStatusError())
			return
		}
		status = pgtype.Text{String: value, Valid: true}
	}
	var tag pgtype.Text
	if value := strings.TrimSpace(req.Tag); value != "" {
		tag = pgtype.Text{String: value, Valid: true}
	}
	if mode == "重命名" {
		tag = pgtype.Text{String: sourceTag, Valid: true}
	}
	var keyword pgtype.Text
	if value := strings.TrimSpace(req.Keyword); value != "" {
		keyword = pgtype.Text{String: value, Valid: true}
	}
	filterPayload := map[string]any{
		"asset_id": uuidToString(asset.ID),
		"source":   strings.TrimSpace(req.Source),
		"tag":      strings.TrimSpace(req.Tag),
		"keyword":  strings.TrimSpace(req.Keyword),
		"status":   strings.TrimSpace(req.Status),
		"limit":    limit,
	}
	if mode == "重命名" {
		filterPayload["tag"] = sourceTag
	}
	inputPayload := map[string]any{
		"mode":       mode,
		"tags":       targetTags,
		"source_tag": sourceTag,
		"target_tag": targetTag,
	}
	operationType := "批量" + mode + "标签"
	if mode == "重命名" {
		operationType = "批量重命名/合并标签"
	}
	job := promptEvaluationCaseBulkTagsJob{
		WorkspaceID:   workspaceUUID,
		Asset:         asset,
		CreatedBy:     parseUUID(userID),
		Source:        source,
		Status:        status,
		Tag:           tag,
		Keyword:       keyword,
		Limit:         limit,
		Mode:          mode,
		TargetTags:    targetTags,
		SourceTag:     sourceTag,
		TargetTag:     targetTag,
		OperationType: operationType,
		FilterPayload: filterPayload,
		InputPayload:  inputPayload,
	}
	executionMode := strings.TrimSpace(req.ExecutionMode)
	if executionMode == "" {
		executionMode = "同步"
	}
	if executionMode != "同步" && executionMode != "后台" {
		writeError(w, http.StatusBadRequest, "execution_mode must be 同步 or 后台")
		return
	}
	if executionMode == "后台" {
		operation, err := h.Queries.CreatePromptEvaluationCaseOperation(r.Context(), db.CreatePromptEvaluationCaseOperationParams{
			WorkspaceID:   workspaceUUID,
			AssetID:       asset.ID,
			OperationType: operationType,
			Filter:        mustJSONBytes(filterPayload),
			Input:         mustJSONBytes(inputPayload),
			ChangedCount:  0,
			SkippedCount:  0,
			SampleCaseIds: mustJSONBytes([]string{}),
			CreatedBy:     parseUUID(userID),
			Status:        pgtype.Text{String: "已入队", Valid: true},
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to queue prompt evaluation case operation")
			return
		}
		go h.runPromptEvaluationCaseBulkTagsBackground(operation.ID, job)
		writeJSON(w, http.StatusAccepted, BulkPromptEvaluationCaseTagsResponse{
			Operation:    promptEvaluationCaseOperationToResponse(operation),
			Cases:        []PromptEvaluationCaseResponse{},
			ChangedCount: 0,
			SkippedCount: 0,
		})
		return
	}

	result, err := h.executePromptEvaluationCaseBulkTags(r.Context(), job, pgtype.UUID{})
	if err != nil {
		slog.Warn("prompt evaluation case bulk tags failed", "error", err, "asset_id", req.AssetID, "workspace_id", workspaceID)
		writeError(w, http.StatusInternalServerError, "failed to run prompt evaluation case bulk tags")
		return
	}
	respCases := make([]PromptEvaluationCaseResponse, len(result.ChangedCases))
	for i, item := range result.ChangedCases {
		respCases[i] = promptEvaluationCaseToResponse(item, nil)
	}
	writeJSON(w, http.StatusOK, BulkPromptEvaluationCaseTagsResponse{
		Operation:    promptEvaluationCaseOperationToResponse(result.Operation),
		Cases:        respCases,
		ChangedCount: result.ChangedCount,
		SkippedCount: result.SkippedCount,
	})
}

func (h *Handler) runPromptEvaluationCaseBulkTagsBackground(operationID pgtype.UUID, job promptEvaluationCaseBulkTagsJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := h.executePromptEvaluationCaseBulkTags(ctx, job, operationID); err != nil {
		slog.Warn("background prompt evaluation case bulk tags failed", "error", err, "operation_id", uuidToString(operationID), "asset_id", uuidToString(job.Asset.ID))
		_, _ = h.Queries.FailPromptEvaluationCaseOperation(ctx, db.FailPromptEvaluationCaseOperationParams{
			ID:           operationID,
			WorkspaceID:  job.WorkspaceID,
			ErrorMessage: err.Error(),
		})
	}
}

func (h *Handler) executePromptEvaluationCaseBulkTags(ctx context.Context, job promptEvaluationCaseBulkTagsJob, operationID pgtype.UUID) (promptEvaluationCaseBulkTagsResult, error) {
	if operationID.Valid {
		if _, err := h.Queries.MarkPromptEvaluationCaseOperationRunning(ctx, db.MarkPromptEvaluationCaseOperationRunningParams{
			ID:          operationID,
			WorkspaceID: job.WorkspaceID,
		}); err != nil {
			return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("mark operation running: %w", err)
		}
	}
	matched, err := h.Queries.ListPromptEvaluationCases(ctx, db.ListPromptEvaluationCasesParams{
		WorkspaceID: job.WorkspaceID,
		AssetID:     job.Asset.ID,
		Status:      job.Status,
		Source:      job.Source,
		Tag:         job.Tag,
		Keyword:     job.Keyword,
		Limit:       pgtype.Int4{Int32: job.Limit, Valid: true},
	})
	if err != nil {
		return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("list prompt evaluation cases: %w", err)
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("start prompt evaluation case bulk transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	changed := make([]db.PromptEvaluationCase, 0, len(matched))
	sampleIDs := make([]string, 0, 20)
	skippedCount := int32(0)
	for _, item := range matched {
		currentTags := stringListFromAny(decodeJSONDefault(item.Tags, []any{}))
		nextTags := bulkPromptEvaluationCaseTags(currentTags, job.TargetTags, job.Mode, job.SourceTag, job.TargetTag)
		if samePromptEvaluationStringList(currentTags, nextTags) {
			skippedCount += 1
			continue
		}
		updated, err := qtx.UpdatePromptEvaluationCase(ctx, db.UpdatePromptEvaluationCaseParams{
			ID:               item.ID,
			WorkspaceID:      job.WorkspaceID,
			AssetID:          item.AssetID,
			PromptID:         item.PromptID,
			CaseIndex:        item.CaseIndex,
			CaseName:         item.CaseName,
			Variables:        item.Variables,
			ExpectedContains: item.ExpectedContains,
			Input:            item.Input,
			Expected:         item.Expected,
			Tags:             mustJSONBytes(nextTags),
			Status:           item.Status,
			Source:           item.Source,
		})
		if err != nil {
			return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("update prompt evaluation case tags: %w", err)
		}
		if err := syncPromptEvaluationDatasetRow(ctx, qtx, job.Asset, updated); err != nil {
			return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("sync prompt evaluation dataset row: %w", err)
		}
		if err := syncPromptEvaluationTestSuiteCase(ctx, qtx, job.Asset, updated); err != nil {
			return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("sync prompt evaluation test suite case: %w", err)
		}
		changed = append(changed, updated)
		if len(sampleIDs) < 20 {
			sampleIDs = append(sampleIDs, uuidToString(updated.ID))
		}
	}

	if len(changed) > 0 {
		allCases, err := qtx.ListPromptEvaluationCases(ctx, db.ListPromptEvaluationCasesParams{
			WorkspaceID: job.WorkspaceID,
			AssetID:     job.Asset.ID,
		})
		if err != nil {
			return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("reload prompt evaluation cases: %w", err)
		}
		payload := normalizePromptEvaluationPayloadObject(promptEvaluationPayloadWithCases(decodePayloadObject(job.Asset.Payload), promptEvaluationPayloadCasesFromCaseRows(allCases)))
		payload["最近批量用例操作"] = map[string]any{
			"operation_type": job.OperationType,
			"changed_count":  len(changed),
			"skipped_count":  skippedCount,
			"tags":           job.TargetTags,
			"source_tag":     job.SourceTag,
			"target_tag":     job.TargetTag,
			"created_at":     time.Now().Format(time.RFC3339),
		}
		payloadBytes := mustJSONBytes(payload)
		profile := promptEvaluationAssetProfileFromPayload(payloadBytes, job.Asset.PromptID, job.Asset.AssetType)
		if _, err = qtx.UpdatePromptEvaluationAsset(ctx, db.UpdatePromptEvaluationAssetParams{
			ID:                       job.Asset.ID,
			WorkspaceID:              job.Asset.WorkspaceID,
			PromptID:                 job.Asset.PromptID,
			Payload:                  payloadBytes,
			StructureSchema:          pgtype.Text{String: profile.StructureSchema, Valid: true},
			StructuredCaseCount:      pgtype.Int4{Int32: profile.StructuredCaseCount, Valid: true},
			StructuredVariableCount:  pgtype.Int4{Int32: profile.StructuredVariableCount, Valid: true},
			StructuredAssertionCount: pgtype.Int4{Int32: profile.StructuredAssertionCount, Valid: true},
			LinkedDatasetCount:       pgtype.Int4{Int32: profile.LinkedDatasetCount, Valid: true},
			LinkedPromptCount:        pgtype.Int4{Int32: profile.LinkedPromptCount, Valid: true},
			EvaluationDimensionCount: pgtype.Int4{Int32: profile.EvaluationDimensionCount, Valid: true},
			ExperimentDimensionCount: pgtype.Int4{Int32: profile.ExperimentDimensionCount, Valid: true},
		}); err != nil {
			return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("update prompt evaluation asset payload: %w", err)
		}
	}

	var operation db.PromptEvaluationCaseOperation
	if operationID.Valid {
		operation, err = qtx.CompletePromptEvaluationCaseOperation(ctx, db.CompletePromptEvaluationCaseOperationParams{
			ID:            operationID,
			WorkspaceID:   job.WorkspaceID,
			ChangedCount:  int32(len(changed)),
			SkippedCount:  skippedCount,
			SampleCaseIds: mustJSONBytes(sampleIDs),
		})
	} else {
		now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
		operation, err = qtx.CreatePromptEvaluationCaseOperation(ctx, db.CreatePromptEvaluationCaseOperationParams{
			WorkspaceID:   job.WorkspaceID,
			AssetID:       job.Asset.ID,
			OperationType: job.OperationType,
			Filter:        mustJSONBytes(job.FilterPayload),
			Input:         mustJSONBytes(job.InputPayload),
			ChangedCount:  int32(len(changed)),
			SkippedCount:  skippedCount,
			SampleCaseIds: mustJSONBytes(sampleIDs),
			CreatedBy:     job.CreatedBy,
			Status:        pgtype.Text{String: "已完成", Valid: true},
			StartedAt:     now,
			CompletedAt:   now,
		})
	}
	if err != nil {
		return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("record prompt evaluation case operation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("commit prompt evaluation case bulk update: %w", err)
	}
	return promptEvaluationCaseBulkTagsResult{
		Operation:    operation,
		ChangedCases: changed,
		ChangedCount: int32(len(changed)),
		SkippedCount: skippedCount,
	}, nil
}

func (h *Handler) ListPromptEvaluationExperimentDimensions(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusGone, "prompt evaluation experiments have been removed; use test suites and evaluation runs")
}

func (h *Handler) ListPromptEvaluationDimensionScores(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var runID pgtype.UUID
	if value := r.URL.Query().Get("run_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "run_id")
		if !ok {
			return
		}
		runID = parsed
	}
	var assetID pgtype.UUID
	if value := r.URL.Query().Get("asset_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "asset_id")
		if !ok {
			return
		}
		assetID = parsed
	}
	var promptID pgtype.UUID
	if value := r.URL.Query().Get("prompt_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "prompt_id")
		if !ok {
			return
		}
		promptID = parsed
	}
	var status pgtype.Text
	if value := r.URL.Query().Get("status"); value != "" {
		if !validPromptEvaluationDimensionScoreStatus(value) {
			writeError(w, http.StatusBadRequest, "status must be 待执行, 已评分 or 无用例")
			return
		}
		status = pgtype.Text{String: value, Valid: true}
	}
	items, err := h.Queries.ListPromptEvaluationDimensionScores(r.Context(), db.ListPromptEvaluationDimensionScoresParams{
		WorkspaceID: workspaceUUID,
		RunID:       runID,
		AssetID:     assetID,
		PromptID:    promptID,
		Status:      status,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation dimension scores")
		return
	}
	resp := make([]PromptEvaluationDimensionScoreResponse, len(items))
	for i, item := range items {
		resp[i] = promptEvaluationDimensionScoreToResponse(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) ListPromptEvaluationDimensionScoreSummaries(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var assetID pgtype.UUID
	if value := r.URL.Query().Get("asset_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "asset_id")
		if !ok {
			return
		}
		assetID = parsed
	}
	var promptID pgtype.UUID
	if value := r.URL.Query().Get("prompt_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "prompt_id")
		if !ok {
			return
		}
		promptID = parsed
	}
	var status pgtype.Text
	if value := r.URL.Query().Get("status"); value != "" {
		if !validPromptEvaluationDimensionScoreStatus(value) {
			writeError(w, http.StatusBadRequest, "status must be 待执行, 已评分 or 无用例")
			return
		}
		status = pgtype.Text{String: value, Valid: true}
	}
	items, err := h.Queries.ListPromptEvaluationDimensionScoreSummaries(r.Context(), db.ListPromptEvaluationDimensionScoreSummariesParams{
		WorkspaceID: workspaceUUID,
		AssetID:     assetID,
		PromptID:    promptID,
		Status:      status,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation dimension score summaries")
		return
	}
	resp := make([]PromptEvaluationDimensionScoreSummaryResponse, len(items))
	for i, item := range items {
		resp[i] = promptEvaluationDimensionScoreSummaryToResponse(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) ListPromptEvaluationDimensionScoreTrends(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var assetID pgtype.UUID
	if value := r.URL.Query().Get("asset_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "asset_id")
		if !ok {
			return
		}
		assetID = parsed
	}
	var promptID pgtype.UUID
	if value := r.URL.Query().Get("prompt_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "prompt_id")
		if !ok {
			return
		}
		promptID = parsed
	}
	var status pgtype.Text
	if value := r.URL.Query().Get("status"); value != "" {
		if !validPromptEvaluationDimensionScoreStatus(value) {
			writeError(w, http.StatusBadRequest, "status must be 待执行, 已评分 or 无用例")
			return
		}
		status = pgtype.Text{String: value, Valid: true}
	}
	var since pgtype.Timestamptz
	if value := r.URL.Query().Get("since"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		since = pgtype.Timestamptz{Time: parsed, Valid: true}
	}
	items, err := h.Queries.ListPromptEvaluationDimensionScoreTrends(r.Context(), db.ListPromptEvaluationDimensionScoreTrendsParams{
		WorkspaceID: workspaceUUID,
		AssetID:     assetID,
		PromptID:    promptID,
		Status:      status,
		Since:       since,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation dimension score trends")
		return
	}
	resp := make([]PromptEvaluationDimensionScoreTrendResponse, len(items))
	for i, item := range items {
		resp[i] = promptEvaluationDimensionScoreTrendToResponse(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func validPromptEvaluationDimensionScoreStatus(status string) bool {
	switch status {
	case "待执行", "已评分", "无用例":
		return true
	default:
		return false
	}
}

func validPromptEvaluationCaseStatus(status string) bool {
	switch status {
	case "启用", "归档", "draft", "approved", "active":
		return true
	default:
		return false
	}
}

func promptEvaluationCaseStatusError() string {
	return "status must be 启用, 归档, draft, approved, or active"
}

func (h *Handler) CreatePromptEvaluationCase(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CreatePromptEvaluationCaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid prompt evaluation case payload")
		return
	}
	assetID, ok := parseUUIDOrBadRequest(w, req.AssetID, "asset_id")
	if !ok {
		return
	}
	asset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{ID: assetID, WorkspaceID: workspaceUUID})
	if err != nil {
		writeError(w, http.StatusBadRequest, "asset_id does not belong to this workspace")
		return
	}
	promptID, ok := h.promptEvaluationPromptID(w, r, workspaceUUID, req.PromptID, asset.PromptID)
	if !ok {
		return
	}
	caseIndex := int32(0)
	if req.CaseIndex != nil {
		caseIndex = *req.CaseIndex
	} else {
		existing, err := h.Queries.ListPromptEvaluationCases(r.Context(), db.ListPromptEvaluationCasesParams{WorkspaceID: workspaceUUID, AssetID: asset.ID})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to allocate prompt evaluation case index")
			return
		}
		caseIndex = int32(len(existing))
	}
	if caseIndex < 0 {
		writeError(w, http.StatusBadRequest, "case_index must be greater than or equal to 0")
		return
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "启用"
	}
	if !validPromptEvaluationCaseStatus(status) {
		writeError(w, http.StatusBadRequest, promptEvaluationCaseStatusError())
		return
	}
	variables, ok := jsonObjectBytesOrDefault(w, req.Variables, "variables", []byte("{}"))
	if !ok {
		return
	}
	expectedContains, ok := jsonArrayBytesOrDefault(w, req.ExpectedContains, "expected_contains", []byte("[]"))
	if !ok {
		return
	}
	input, ok := jsonObjectBytesOrDefault(w, req.Input, "input", []byte("{}"))
	if !ok {
		return
	}
	expected, ok := jsonObjectBytesOrDefault(w, req.Expected, "expected", []byte("{}"))
	if !ok {
		return
	}
	tags, ok := jsonArrayBytesOrDefault(w, req.Tags, "tags", []byte("[]"))
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation case transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	created, err := qtx.CreatePromptEvaluationCase(r.Context(), db.CreatePromptEvaluationCaseParams{
		WorkspaceID:      workspaceUUID,
		AssetID:          asset.ID,
		PromptID:         promptID,
		CaseIndex:        caseIndex,
		CaseName:         strings.TrimSpace(req.CaseName),
		Variables:        variables,
		ExpectedContains: expectedContains,
		Input:            input,
		Expected:         expected,
		Tags:             tags,
		Status:           status,
		Source:           "manual",
		CreatedBy:        parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation case")
		return
	}
	assertions, err := syncPromptEvaluationCaseAssertions(r.Context(), qtx, created, expectedContains)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation case assertions")
		return
	}
	if err := syncPromptEvaluationDatasetRow(r.Context(), qtx, asset, created); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation dataset row")
		return
	}
	if err := syncPromptEvaluationTestSuiteCase(r.Context(), qtx, asset, created); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation test suite case")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation case")
		return
	}
	writeJSON(w, http.StatusCreated, promptEvaluationCaseToResponse(created, assertions))
}

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
	defer tx.Rollback(r.Context())
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
	defer tx.Rollback(r.Context())
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
	defer tx.Rollback(r.Context())
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

func validPromptEvaluationCaseSortBy(value string) bool {
	switch value {
	case "case_index", "case_name", "source", "created_at", "updated_at":
		return true
	default:
		return false
	}
}

func encodePromptEvaluationCaseCursor(offset int32, sortBy string, sortDirection string, item db.PromptEvaluationCase) string {
	cursor := promptEvaluationCaseCursor{
		Offset:        offset,
		SortBy:        sortBy,
		SortDirection: sortDirection,
		LastID:        uuidToString(item.ID),
		CaseIndex:     item.CaseIndex,
		CaseName:      item.CaseName,
		Source:        item.Source,
	}
	if item.CreatedAt.Valid {
		cursor.CreatedAt = item.CreatedAt.Time.Format(time.RFC3339Nano)
	}
	if item.UpdatedAt.Valid {
		cursor.UpdatedAt = item.UpdatedAt.Time.Format(time.RFC3339Nano)
	}
	payload, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodePromptEvaluationCaseCursor(w http.ResponseWriter, value string) (promptEvaluationCaseCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "cursor is invalid")
		return promptEvaluationCaseCursor{}, false
	}
	var cursor promptEvaluationCaseCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.Offset < 0 || cursor.LastID == "" || !validPromptEvaluationCaseSortBy(cursor.SortBy) || (cursor.SortDirection != "asc" && cursor.SortDirection != "desc") {
		writeError(w, http.StatusBadRequest, "cursor is invalid")
		return promptEvaluationCaseCursor{}, false
	}
	return cursor, true
}

func compactStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		item := strings.TrimSpace(value)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	return result
}

func samePromptEvaluationStringList(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func bulkPromptEvaluationCaseTags(current []string, target []string, mode string, sourceTag string, targetTag string) []string {
	if mode == "重命名" {
		next := make([]string, 0, len(current))
		for _, tag := range current {
			if tag == sourceTag {
				next = append(next, targetTag)
				continue
			}
			next = append(next, tag)
		}
		return compactStrings(next)
	}
	if mode == "移除" {
		removing := map[string]bool{}
		for _, tag := range target {
			removing[tag] = true
		}
		next := make([]string, 0, len(current))
		for _, tag := range current {
			if !removing[tag] {
				next = append(next, tag)
			}
		}
		return next
	}
	return compactStrings(append(append([]string{}, current...), target...))
}

func promptEvaluationPayloadCasesFromCaseRows(rows []db.PromptEvaluationCase) []map[string]any {
	cases := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if row.Source != "payload" {
			continue
		}
		cases = append(cases, map[string]any{
			"name":              row.CaseName,
			"case_name":         row.CaseName,
			"名称":                row.CaseName,
			"variables":         decodeJSONDefault(row.Variables, map[string]any{}),
			"变量":                decodeJSONDefault(row.Variables, map[string]any{}),
			"expected_contains": decodeJSONDefault(row.ExpectedContains, []any{}),
			"期望包含":              decodeJSONDefault(row.ExpectedContains, []any{}),
			"input":             decodeJSONDefault(row.Input, map[string]any{}),
			"输入":                decodeJSONDefault(row.Input, map[string]any{}),
			"expected":          decodeJSONDefault(row.Expected, map[string]any{}),
			"期望":                decodeJSONDefault(row.Expected, map[string]any{}),
			"tags":              decodeJSONDefault(row.Tags, []any{}),
			"标签":                decodeJSONDefault(row.Tags, []any{}),
			"status":            row.Status,
			"状态":                row.Status,
		})
	}
	return cases
}

func (h *Handler) UpdatePromptEvaluationCase(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	caseID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation case id")
	if !ok {
		return
	}
	current, err := h.Queries.GetPromptEvaluationCaseInWorkspace(r.Context(), db.GetPromptEvaluationCaseInWorkspaceParams{ID: caseID, WorkspaceID: workspaceUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation case not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation case")
		return
	}
	var req UpdatePromptEvaluationCaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid prompt evaluation case payload")
		return
	}
	assetID := current.AssetID
	if req.AssetID != nil {
		parsed, ok := parseUUIDOrBadRequest(w, *req.AssetID, "asset_id")
		if !ok {
			return
		}
		assetID = parsed
	}
	asset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{ID: assetID, WorkspaceID: workspaceUUID})
	if err != nil {
		writeError(w, http.StatusBadRequest, "asset_id does not belong to this workspace")
		return
	}
	promptID, ok := h.promptEvaluationPromptID(w, r, workspaceUUID, req.PromptID, current.PromptID)
	if !ok {
		return
	}
	if len(req.PromptID) == 0 && !promptID.Valid {
		promptID = asset.PromptID
	}
	caseIndex := current.CaseIndex
	if req.CaseIndex != nil {
		caseIndex = *req.CaseIndex
	}
	if caseIndex < 0 {
		writeError(w, http.StatusBadRequest, "case_index must be greater than or equal to 0")
		return
	}
	caseName := current.CaseName
	if req.CaseName != nil {
		caseName = strings.TrimSpace(*req.CaseName)
	}
	status := current.Status
	if req.Status != nil {
		status = strings.TrimSpace(*req.Status)
	}
	if !validPromptEvaluationCaseStatus(status) {
		writeError(w, http.StatusBadRequest, promptEvaluationCaseStatusError())
		return
	}
	variables, ok := jsonObjectBytesForUpdate(w, req.Variables, "variables", current.Variables)
	if !ok {
		return
	}
	expectedContains, ok := jsonArrayBytesForUpdate(w, req.ExpectedContains, "expected_contains", current.ExpectedContains)
	if !ok {
		return
	}
	input, ok := jsonObjectBytesForUpdate(w, req.Input, "input", current.Input)
	if !ok {
		return
	}
	expected, ok := jsonObjectBytesForUpdate(w, req.Expected, "expected", current.Expected)
	if !ok {
		return
	}
	tags, ok := jsonArrayBytesForUpdate(w, req.Tags, "tags", current.Tags)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation case transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.UpdatePromptEvaluationCase(r.Context(), db.UpdatePromptEvaluationCaseParams{
		ID:               current.ID,
		WorkspaceID:      workspaceUUID,
		AssetID:          asset.ID,
		PromptID:         promptID,
		CaseIndex:        caseIndex,
		CaseName:         caseName,
		Variables:        variables,
		ExpectedContains: expectedContains,
		Input:            input,
		Expected:         expected,
		Tags:             tags,
		Status:           status,
		Source:           current.Source,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update prompt evaluation case")
		return
	}
	assertions, err := syncPromptEvaluationCaseAssertions(r.Context(), qtx, updated, expectedContains)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update prompt evaluation case assertions")
		return
	}
	if err := syncPromptEvaluationDatasetRow(r.Context(), qtx, asset, updated); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation dataset row")
		return
	}
	if err := syncPromptEvaluationTestSuiteCase(r.Context(), qtx, asset, updated); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync prompt evaluation test suite case")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation case")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationCaseToResponse(updated, assertions))
}

func (h *Handler) DeletePromptEvaluationCase(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	caseID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation case id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetPromptEvaluationCaseInWorkspace(r.Context(), db.GetPromptEvaluationCaseInWorkspaceParams{ID: caseID, WorkspaceID: workspaceUUID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation case not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation case")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation case transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := deletePromptEvaluationDatasetRowsForCase(r.Context(), qtx, workspaceUUID, caseID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete prompt evaluation dataset row")
		return
	}
	if err := deletePromptEvaluationTestSuiteCasesForCase(r.Context(), qtx, workspaceUUID, caseID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete prompt evaluation test suite case")
		return
	}
	if err := qtx.DeletePromptEvaluationCase(r.Context(), db.DeletePromptEvaluationCaseParams{ID: caseID, WorkspaceID: workspaceUUID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete prompt evaluation case")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation case deletion")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

