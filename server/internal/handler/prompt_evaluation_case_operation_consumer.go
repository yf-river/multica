package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	promptEvaluationCaseOperationRequestedEvent = "prompt_evaluation_case_operation:requested"
	promptEvaluationCaseOperationConsumer       = "prompt_evaluation_case_operation"
)

func (h *Handler) RegisterPromptEvaluationCaseOperationConsumer(dispatcher *eventoutbox.Dispatcher) error {
	if dispatcher == nil {
		return errors.New("prompt evaluation case operation consumer requires an event dispatcher")
	}
	return dispatcher.RegisterWithDeadLetter(
		promptEvaluationCaseOperationRequestedEvent,
		promptEvaluationCaseOperationConsumer,
		h.consumePromptEvaluationCaseOperation,
		h.failPromptEvaluationCaseOperation,
	)
}

func (h *Handler) consumePromptEvaluationCaseOperation(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	operationID, workspaceID, err := promptEvaluationCaseOperationEventIDs(event)
	if err != nil {
		return nil, err
	}
	operation, err := queries.GetPromptEvaluationCaseOperationInWorkspace(ctx, db.GetPromptEvaluationCaseOperationInWorkspaceParams{
		ID:          operationID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load prompt evaluation case operation: %w", err)
	}
	if operation.Status == "已完成" {
		return nil, nil
	}
	asset, err := queries.GetPromptEvaluationAssetInWorkspace(ctx, db.GetPromptEvaluationAssetInWorkspaceParams{
		ID:          operation.AssetID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("load prompt evaluation case operation asset: %w", err)
	}
	job, err := promptEvaluationCaseBulkTagsJobFromOperation(operation, asset)
	if err != nil {
		return nil, err
	}
	if _, err := executePromptEvaluationCaseBulkTagsInTx(ctx, queries, job, operation.ID); err != nil {
		return nil, err
	}
	return nil, nil
}

func promptEvaluationCaseOperationEventIDs(event events.Event) (pgtype.UUID, pgtype.UUID, error) {
	var payload struct {
		OperationID string `json:"operation_id"`
	}
	rawPayload, err := json.Marshal(event.Payload)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("marshal prompt evaluation case operation event: %w", err)
	}
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("decode prompt evaluation case operation event: %w", err)
	}
	if strings.TrimSpace(payload.OperationID) == "" {
		return pgtype.UUID{}, pgtype.UUID{}, errors.New("decode prompt evaluation case operation event: operation_id is required")
	}
	operationID, err := util.ParseUUID(payload.OperationID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("parse prompt evaluation case operation ID: %w", err)
	}
	workspaceID, err := util.ParseUUID(event.WorkspaceID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("parse prompt evaluation case operation workspace ID: %w", err)
	}
	return operationID, workspaceID, nil
}

func (h *Handler) failPromptEvaluationCaseOperation(ctx context.Context, queries *db.Queries, event events.Event, cause error) error {
	operationID, workspaceID, err := promptEvaluationCaseOperationEventIDs(event)
	if err != nil {
		return err
	}
	message := cause.Error()
	if len(message) > 2000 {
		message = message[:2000]
	}
	_, err = queries.FailPromptEvaluationCaseOperation(ctx, db.FailPromptEvaluationCaseOperationParams{
		ID:           operationID,
		WorkspaceID:  workspaceID,
		ErrorMessage: message,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("fail prompt evaluation case operation: %w", err)
	}
	return nil
}

func promptEvaluationCaseBulkTagsJobFromOperation(operation db.PromptEvaluationCaseOperation, asset db.PromptEvaluationAsset) (promptEvaluationCaseBulkTagsJob, error) {
	var filter struct {
		Source  string `json:"source"`
		Tag     string `json:"tag"`
		Keyword string `json:"keyword"`
		Status  string `json:"status"`
		Limit   int32  `json:"limit"`
	}
	var input struct {
		Mode      string   `json:"mode"`
		Tags      []string `json:"tags"`
		SourceTag string   `json:"source_tag"`
		TargetTag string   `json:"target_tag"`
	}
	if err := json.Unmarshal(operation.Filter, &filter); err != nil {
		return promptEvaluationCaseBulkTagsJob{}, fmt.Errorf("decode prompt evaluation case operation filter: %w", err)
	}
	if err := json.Unmarshal(operation.Input, &input); err != nil {
		return promptEvaluationCaseBulkTagsJob{}, fmt.Errorf("decode prompt evaluation case operation input: %w", err)
	}
	if filter.Limit < 1 || filter.Limit > 500 {
		return promptEvaluationCaseBulkTagsJob{}, fmt.Errorf("prompt evaluation case operation limit %d is invalid", filter.Limit)
	}
	if input.Mode != "追加" && input.Mode != "移除" && input.Mode != "重命名" {
		return promptEvaluationCaseBulkTagsJob{}, fmt.Errorf("prompt evaluation case operation mode %q is invalid", input.Mode)
	}
	filterPayload := map[string]any{}
	inputPayload := map[string]any{}
	if err := json.Unmarshal(operation.Filter, &filterPayload); err != nil {
		return promptEvaluationCaseBulkTagsJob{}, fmt.Errorf("decode prompt evaluation case operation filter payload: %w", err)
	}
	if err := json.Unmarshal(operation.Input, &inputPayload); err != nil {
		return promptEvaluationCaseBulkTagsJob{}, fmt.Errorf("decode prompt evaluation case operation input payload: %w", err)
	}
	return promptEvaluationCaseBulkTagsJob{
		WorkspaceID:   operation.WorkspaceID,
		Asset:         asset,
		CreatedBy:     operation.CreatedBy,
		Source:        optionalPromptEvaluationCaseOperationText(filter.Source),
		Status:        optionalPromptEvaluationCaseOperationText(filter.Status),
		Tag:           optionalPromptEvaluationCaseOperationText(filter.Tag),
		Keyword:       optionalPromptEvaluationCaseOperationText(filter.Keyword),
		Limit:         filter.Limit,
		Mode:          input.Mode,
		TargetTags:    input.Tags,
		SourceTag:     input.SourceTag,
		TargetTag:     input.TargetTag,
		OperationType: operation.OperationType,
		FilterPayload: filterPayload,
		InputPayload:  inputPayload,
	}, nil
}

func optionalPromptEvaluationCaseOperationText(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	return pgtype.Text{String: value, Valid: value != ""}
}
