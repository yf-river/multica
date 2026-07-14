package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	resourceTypeProject                = "project"
	resourceTypeSquad                  = "squad"
	resourceTypeAgent                  = "agent"
	resourceTypeSkill                  = "skill"
	resourceTypeAttachment             = "attachment"
	resourceTypeQuickCreate            = "quick_create"
	resourceTypeIssue                  = "issue"
	resourceTypeComment                = "comment"
	resourceTypeAutopilotTrigger       = "autopilot_trigger"
	resourceTypeIssueRerun             = "issue_rerun"
	resourceTypeWorkspace              = "workspace"
	resourceTypePromptLibraryItem      = "prompt_library_item"
	resourceTypePromptLibraryVersion   = "prompt_library_version"
	resourceTypePromptLibraryTrial     = "prompt_library_trial"
	resourceTypeWorkspaceMember        = "workspace_member"
	resourceTypeAgentPlayground        = "agent_playground_experiment"
	resourceTypePromptEvaluationRun    = "prompt_evaluation_agent_run"
	resourceTypePromptLocalRun         = "prompt_evaluation_local_run"
	resourceTypePromptReEvalAsset      = "prompt_evaluation_re_eval_asset"
	resourceTypePromptCandidate        = "prompt_evaluation_candidate"
	resourceTypePromptPublish          = "prompt_evaluation_candidate_publish"
	resourceTypePromptReject           = "prompt_evaluation_candidate_reject"
	resourceTypeRuntimeProfile         = "runtime_profile"
	resourceTypeLabel                  = "label"
	resourceTypeProjectResource        = "project_resource"
	resourceTypePromptEvalAsset        = "prompt_evaluation_asset"
	resourceTypePromptEvalCase         = "prompt_evaluation_case"
	resourceTypePromptTraceImport      = "prompt_evaluation_trace_import"
	resourceTypePromptDatasetVersion   = "prompt_evaluation_dataset_version"
	resourceTypePromptEvidenceSnapshot = "prompt_evaluation_evidence_snapshot"
	resourceTypePromptEvidenceBatch    = "prompt_evaluation_evidence_batch"
	resourceTypePromptDatasetRestore   = "prompt_evaluation_dataset_restore"
	resourceTypePromptSkillInventory   = "prompt_evaluation_skill_inventory"
	resourceTypePromptSkillSnapshot    = "prompt_evaluation_skill_snapshot"
	resourceTypePromptSkillCaseDrafts  = "prompt_evaluation_skill_case_drafts"
	resourceTypePromptSkillApply       = "prompt_evaluation_skill_apply"
)

var errResourceCreateIdempotencyConflict = errors.New("resource create idempotency conflict")

func reserveResourceCreateRequest(
	ctx context.Context,
	queries *db.Queries,
	workspaceID, actorID pgtype.UUID,
	resourceType string,
	idempotencyKey pgtype.UUID,
	requestHash string,
) error {
	_, err := queries.ReserveResourceCreateRequest(ctx, db.ReserveResourceCreateRequestParams{
		WorkspaceID: workspaceID, ActorID: actorID, ResourceType: resourceType,
		IdempotencyKey: idempotencyKey, RequestHash: requestHash,
	})
	return err
}

func loadResourceCreateReplay[T any](
	ctx context.Context,
	queries *db.Queries,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	resourceType string,
	idempotencyKey pgtype.UUID,
	requestHash string,
	isValid func(T) bool,
) (T, bool, error) {
	var response T
	record, err := queries.GetResourceCreateRequest(ctx, db.GetResourceCreateRequestParams{
		WorkspaceID:    workspaceID,
		ActorID:        actorID,
		ResourceType:   resourceType,
		IdempotencyKey: idempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return response, false, nil
	}
	if err != nil {
		return response, false, err
	}
	if record.RequestHash != requestHash {
		return response, false, errResourceCreateIdempotencyConflict
	}
	if len(record.ResponseBody) == 0 || !record.CompletedAt.Valid || !record.ResourceID.Valid {
		return response, false, fmt.Errorf("%s create request is incomplete", resourceType)
	}
	if err := json.Unmarshal(record.ResponseBody, &response); err != nil {
		return response, false, fmt.Errorf("decode %s create replay: %w", resourceType, err)
	}
	if !isValid(response) {
		return response, false, fmt.Errorf("%s create replay has no resource id", resourceType)
	}
	return response, true, nil
}

func loadReplayAfterReservationConflict[T any](
	ctx context.Context,
	tx pgx.Tx,
	loadReplay func() (T, bool, error),
) (T, error) {
	var response T
	_ = tx.Rollback(ctx)
	replay, found, err := loadReplay()
	if err != nil {
		return response, err
	}
	if !found {
		return response, errors.New("resource create replay disappeared after reservation conflict")
	}
	return replay, nil
}

func completeResourceCreateRequest(
	ctx context.Context,
	queries *db.Queries,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	resourceType string,
	idempotencyKey pgtype.UUID,
	requestHash string,
	resourceID pgtype.UUID,
	response any,
) error {
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode %s response: %w", resourceType, err)
	}
	_, err = queries.CompleteResourceCreateRequest(ctx, db.CompleteResourceCreateRequestParams{
		WorkspaceID: workspaceID, ActorID: actorID, ResourceType: resourceType,
		IdempotencyKey: idempotencyKey, RequestHash: requestHash,
		ResourceID: resourceID, ResponseBody: body,
	})
	if err != nil {
		return fmt.Errorf("complete %s request: %w", resourceType, err)
	}
	return nil
}
