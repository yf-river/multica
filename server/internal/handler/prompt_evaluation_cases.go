package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
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
		tx, err := h.TxStarter.Begin(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to start prompt evaluation case operation")
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()
		qtx := h.Queries.WithTx(tx)
		operation, err := qtx.CreatePromptEvaluationCaseOperation(r.Context(), db.CreatePromptEvaluationCaseOperationParams{
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
		if _, err := eventoutbox.Enqueue(r.Context(), qtx, events.Event{
			Type:        promptEvaluationCaseOperationRequestedEvent,
			WorkspaceID: workspaceID,
			StreamKey:   "prompt_evaluation_case_operation:" + uuidToString(operation.ID),
			ActorType:   "member",
			ActorID:     userID,
			Payload: map[string]any{
				"operation_id": uuidToString(operation.ID),
			},
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to queue prompt evaluation case operation event")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to commit prompt evaluation case operation")
			return
		}
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

func (h *Handler) executePromptEvaluationCaseBulkTags(ctx context.Context, job promptEvaluationCaseBulkTagsJob, operationID pgtype.UUID) (promptEvaluationCaseBulkTagsResult, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("start prompt evaluation case bulk transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.Queries.WithTx(tx)
	result, err := executePromptEvaluationCaseBulkTagsInTx(ctx, qtx, job, operationID)
	if err != nil {
		return promptEvaluationCaseBulkTagsResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("commit prompt evaluation case bulk update: %w", err)
	}
	return result, nil
}

func executePromptEvaluationCaseBulkTagsInTx(ctx context.Context, queries *db.Queries, job promptEvaluationCaseBulkTagsJob, operationID pgtype.UUID) (promptEvaluationCaseBulkTagsResult, error) {
	if operationID.Valid {
		operation, err := queries.MarkPromptEvaluationCaseOperationRunning(ctx, db.MarkPromptEvaluationCaseOperationRunningParams{
			ID:          operationID,
			WorkspaceID: job.WorkspaceID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			operation, err = queries.GetPromptEvaluationCaseOperationInWorkspace(ctx, db.GetPromptEvaluationCaseOperationInWorkspaceParams{
				ID:          operationID,
				WorkspaceID: job.WorkspaceID,
			})
			if err == nil && operation.Status == "已完成" {
				return promptEvaluationCaseBulkTagsResult{Operation: operation}, nil
			}
			if err == nil {
				err = fmt.Errorf("operation status %q is not claimable", operation.Status)
			}
		}
		if err != nil {
			return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("mark operation running: %w", err)
		}
	}
	matched, err := queries.ListPromptEvaluationCases(ctx, db.ListPromptEvaluationCasesParams{
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
		updated, err := queries.UpdatePromptEvaluationCase(ctx, db.UpdatePromptEvaluationCaseParams{
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
		if err := syncPromptEvaluationDatasetRow(ctx, queries, job.Asset, updated); err != nil {
			return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("sync prompt evaluation dataset row: %w", err)
		}
		if err := syncPromptEvaluationTestSuiteCase(ctx, queries, job.Asset, updated); err != nil {
			return promptEvaluationCaseBulkTagsResult{}, fmt.Errorf("sync prompt evaluation test suite case: %w", err)
		}
		changed = append(changed, updated)
		if len(sampleIDs) < 20 {
			sampleIDs = append(sampleIDs, uuidToString(updated.ID))
		}
	}

	if len(changed) > 0 {
		allCases, err := queries.ListPromptEvaluationCases(ctx, db.ListPromptEvaluationCasesParams{
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
		if _, err = queries.UpdatePromptEvaluationAsset(ctx, db.UpdatePromptEvaluationAssetParams{
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
		operation, err = queries.CompletePromptEvaluationCaseOperation(ctx, db.CompletePromptEvaluationCaseOperationParams{
			ID:            operationID,
			WorkspaceID:   job.WorkspaceID,
			ChangedCount:  int32(len(changed)),
			SkippedCount:  skippedCount,
			SampleCaseIds: mustJSONBytes(sampleIDs),
		})
	} else {
		now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
		operation, err = queries.CreatePromptEvaluationCaseOperation(ctx, db.CreatePromptEvaluationCaseOperationParams{
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
	return promptEvaluationCaseBulkTagsResult{
		Operation:    operation,
		ChangedCases: changed,
		ChangedCount: int32(len(changed)),
		SkippedCount: skippedCount,
	}, nil
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
	defer func() { _ = tx.Rollback(r.Context()) }()
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
	defer func() { _ = tx.Rollback(r.Context()) }()
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
	defer func() { _ = tx.Rollback(r.Context()) }()
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
