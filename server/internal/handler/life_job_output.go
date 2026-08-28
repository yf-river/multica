package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type lifeJobEvidenceOutput struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	Excerpt    string `json:"excerpt"`
	ObservedAt string `json:"observed_at"`
	Stance     string `json:"stance"`
}

type lifeJobMemoryOutput struct {
	Kind        string                  `json:"kind"`
	Content     string                  `json:"content"`
	Confidence  float64                 `json:"confidence"`
	Urgency     float64                 `json:"urgency"`
	Uncertainty string                  `json:"uncertainty"`
	Evidence    []lifeJobEvidenceOutput `json:"evidence"`
}

type lifeJobTopicOutput struct {
	ID          string                  `json:"topic_id"`
	Title       string                  `json:"title"`
	Summary     string                  `json:"summary"`
	Status      string                  `json:"status"`
	Confidence  float64                 `json:"confidence"`
	Uncertainty string                  `json:"uncertainty"`
	MemoryIDs   []string                `json:"memory_ids"`
	Relations   []string                `json:"relations"`
	Evidence    []lifeJobEvidenceOutput `json:"evidence"`
}

type lifeJobCommitmentOutput struct {
	Content        string                  `json:"content"`
	SourceMemoryID string                  `json:"source_memory_id"`
	DueAt          string                  `json:"due_at"`
	RevisitAfter   string                  `json:"revisit_after"`
	Evidence       []lifeJobEvidenceOutput `json:"evidence"`
}

type lifeJobThoughtOutput struct {
	Type     string                  `json:"type"`
	Title    string                  `json:"title"`
	Content  string                  `json:"content"`
	Metadata map[string]any          `json:"metadata"`
	Evidence []lifeJobEvidenceOutput `json:"evidence"`
}

type lifeJobRelationshipOutput struct {
	Type              string                  `json:"type"`
	Status            string                  `json:"status"`
	UserPosition      string                  `json:"user_position"`
	CompanionPosition string                  `json:"companion_position"`
	Context           string                  `json:"context"`
	RevisitAfter      string                  `json:"revisit_after"`
	Evidence          []lifeJobEvidenceOutput `json:"evidence"`
}

type lifeJobActionProposalOutput struct {
	ProposalType string                  `json:"proposal_type"`
	Title        string                  `json:"title"`
	Summary      string                  `json:"summary"`
	Payload      map[string]any          `json:"payload"`
	ExpiresAt    string                  `json:"expires_at"`
	Evidence     []lifeJobEvidenceOutput `json:"evidence"`
}

type lifeJobProactiveOutput struct {
	Status          string                  `json:"status"`
	TriggerSource   string                  `json:"trigger_source"`
	Reason          string                  `json:"reason"`
	Message         string                  `json:"message"`
	ContextSnapshot map[string]any          `json:"context_snapshot"`
	Evidence        []lifeJobEvidenceOutput `json:"evidence"`
}

type lifeJobExperimentObservationOutput struct {
	RoundID    string `json:"round_id"`
	MaterialID string `json:"material_id"`
	Type       string `json:"type"`
	Content    string `json:"content"`
	ObservedAt string `json:"observed_at"`
}

type lifeJobObserverJudgementOutput struct {
	Status      string                  `json:"status"`
	Title       string                  `json:"title"`
	Content     string                  `json:"content"`
	Evidence    []lifeJobEvidenceOutput `json:"evidence"`
	Confidence  float64                 `json:"confidence"`
	Uncertainty string                  `json:"uncertainty"`
}

type lifeJobChronicleOutput struct {
	PeriodKind         string                  `json:"period_kind"`
	PeriodStart        string                  `json:"period_start"`
	PeriodEnd          string                  `json:"period_end"`
	Facts              string                  `json:"facts"`
	Feelings           string                  `json:"feelings"`
	UnderstandingThen  string                  `json:"understanding_then"`
	UnderstandingLater string                  `json:"understanding_later"`
	Actions            string                  `json:"actions"`
	Evidence           []lifeJobEvidenceOutput `json:"evidence"`
}

type lifeJobExperimentReviewOutput struct {
	RoundID             string                  `json:"round_id"`
	Outcome             string                  `json:"outcome"`
	Feelings            string                  `json:"feelings"`
	Burden              string                  `json:"burden"`
	CompanionCorrection string                  `json:"companion_correction"`
	ModuleProposal      map[string]any          `json:"module_proposal"`
	Evidence            []lifeJobEvidenceOutput `json:"evidence"`
}

type lifeJobObservationTopicOutput struct {
	ID           string   `json:"topic_id"`
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	Status       string   `json:"status"`
	JudgementIDs []string `json:"judgement_ids"`
}

type lifeJobUpgradeEvaluationOutput struct {
	EvaluationID        string                  `json:"evaluation_id"`
	Status              string                  `json:"status"`
	Result              map[string]any          `json:"result"`
	RollbackRecommended bool                    `json:"rollback_recommended"`
	Evidence            []lifeJobEvidenceOutput `json:"evidence"`
}

type lifeJobProactiveAssessmentOutput struct {
	CheckID              string `json:"check_id"`
	ValueAssessment      string `json:"value_assessment"`
	MinimumIntervalHours int32  `json:"minimum_interval_hours"`
}

type lifeCognitionOutput struct {
	Summary                string                               `json:"summary"`
	MemoryCandidates       []lifeJobMemoryOutput                `json:"memory_candidates"`
	Topics                 []lifeJobTopicOutput                 `json:"topics"`
	Commitments            []lifeJobCommitmentOutput            `json:"commitments"`
	InternalThoughts       []lifeJobThoughtOutput               `json:"internal_thoughts"`
	RelationshipEvents     []lifeJobRelationshipOutput          `json:"relationship_events"`
	ActionProposals        []lifeJobActionProposalOutput        `json:"action_proposals"`
	ProactiveDecision      *lifeJobProactiveOutput              `json:"proactive_decision"`
	ProactiveAssessment    *lifeJobProactiveAssessmentOutput    `json:"proactive_assessment"`
	ExperimentObservations []lifeJobExperimentObservationOutput `json:"experiment_observations"`
	ExperimentReview       *lifeJobExperimentReviewOutput       `json:"experiment_review"`
	ObserverJudgements     []lifeJobObserverJudgementOutput     `json:"observer_judgements"`
	ObservationTopics      []lifeJobObservationTopicOutput      `json:"observation_topics"`
	Chronicles             []lifeJobChronicleOutput             `json:"chronicles"`
	UpgradeEvaluation      *lifeJobUpgradeEvaluationOutput      `json:"upgrade_evaluation"`
}

type lifeJobOutputError struct {
	message string
}

func (e lifeJobOutputError) Error() string {
	return e.message
}

func invalidLifeJobOutput(format string, args ...any) error {
	return lifeJobOutputError{message: fmt.Sprintf(format, args...)}
}

func validateLifeJobOutput(jobType string, output lifeCognitionOutput) error {
	provided := map[string]bool{
		"memory_candidates":       len(output.MemoryCandidates) > 0,
		"topics":                  len(output.Topics) > 0,
		"commitments":             len(output.Commitments) > 0,
		"internal_thoughts":       len(output.InternalThoughts) > 0,
		"relationship_events":     len(output.RelationshipEvents) > 0,
		"action_proposals":        len(output.ActionProposals) > 0,
		"proactive_decision":      output.ProactiveDecision != nil,
		"proactive_assessment":    output.ProactiveAssessment != nil,
		"experiment_observations": len(output.ExperimentObservations) > 0,
		"experiment_review":       output.ExperimentReview != nil,
		"observer_judgements":     len(output.ObserverJudgements) > 0,
		"observation_topics":      len(output.ObservationTopics) > 0,
		"chronicles":              len(output.Chronicles) > 0,
		"upgrade_evaluation":      output.UpgradeEvaluation != nil,
	}
	allowed := map[string]map[string]bool{
		"understand_materials":  {"memory_candidates": true, "topics": true, "commitments": true, "internal_thoughts": true, "relationship_events": true, "action_proposals": true, "proactive_decision": true, "chronicles": true},
		"review_memories":       {"topics": true, "internal_thoughts": true, "action_proposals": true},
		"develop_thought":       {"internal_thoughts": true, "action_proposals": true},
		"proactive_check":       {"proactive_decision": true},
		"proactive_review":      {"proactive_assessment": true},
		"experiment_check":      {"experiment_observations": true, "experiment_review": true, "action_proposals": true},
		"observer_run":          {"observer_judgements": true},
		"observation_aggregate": {"observation_topics": true},
		"chronicle_generate":    {"chronicles": true},
		"relationship_reunion":  {"memory_candidates": true, "topics": true, "commitments": true, "internal_thoughts": true, "relationship_events": true, "action_proposals": true, "proactive_decision": true},
		"upgrade_evaluation":    {"upgrade_evaluation": true},
	}[jobType]
	if allowed == nil {
		return invalidLifeJobOutput("unsupported life cognition job type %q", jobType)
	}
	for field, present := range provided {
		if present && !allowed[field] {
			return invalidLifeJobOutput("%s is not allowed for %s", field, jobType)
		}
	}
	return nil
}

func validLifeEvidenceSourceType(value string) bool {
	switch value {
	case "chat_message", "task", "comment", "project", "manual", "external", "memory", "experiment_round":
		return true
	default:
		return false
	}
}

func recordLifeDerivations(ctx context.Context, q *db.Queries, scope lifeJobTaskScope, targetType string, targetID pgtype.UUID, evidence []lifeJobEvidenceOutput) error {
	for _, item := range evidence {
		if !validLifeEvidenceSourceType(item.SourceType) || strings.TrimSpace(item.SourceID) == "" {
			return fmt.Errorf("invalid derivation evidence for %s", targetType)
		}
		if err := q.RecordLifeDerivation(ctx, db.RecordLifeDerivationParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID,
			SourceType: item.SourceType, SourceID: strings.TrimSpace(item.SourceID),
			TargetType: targetType, TargetID: targetID, JobID: scope.job.ID,
		}); err != nil {
			return err
		}
	}
	return nil
}

func lifeEvidenceWasForgotten(ctx context.Context, q *db.Queries, scope lifeJobTaskScope, evidence []lifeJobEvidenceOutput) (bool, error) {
	for _, item := range evidence {
		if !validLifeEvidenceSourceType(item.SourceType) || strings.TrimSpace(item.SourceID) == "" {
			return false, fmt.Errorf("invalid life evidence")
		}
		if item.SourceType == "memory" {
			id, err := util.ParseUUID(item.SourceID)
			if err != nil {
				return false, err
			}
			if _, err := q.GetLifeMemory(ctx, db.GetLifeMemoryParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID}); errors.Is(err, pgx.ErrNoRows) {
				return true, nil
			} else if err != nil {
				return false, err
			}
			continue
		}
		material, err := q.GetLatestLifeMaterialBySource(ctx, db.GetLatestLifeMaterialBySourceParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, SourceType: item.SourceType, SourceKey: item.SourceID})
		if errors.Is(err, pgx.ErrNoRows) {
			if id, parseErr := util.ParseUUID(item.SourceID); parseErr == nil {
				material, err = q.GetLifeMaterialForUser(ctx, db.GetLifeMaterialForUserParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID})
			}
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		digest := sha256.Sum256([]byte(material.Content))
		blocked, err := q.IsLifeMaterialForgotten(ctx, db.IsLifeMaterialForgottenParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, SourceType: material.SourceType, SourceKey: material.SourceKey, ContentHash: fmt.Sprintf("%x", digest[:])})
		if err != nil {
			return false, err
		}
		if blocked {
			return true, nil
		}
	}
	return false, nil
}

func (h *Handler) completeLifeCognitionJob(ctx context.Context, scope lifeJobTaskScope, raw []byte) (db.LifeCognitionJob, error) {
	var output lifeCognitionOutput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return db.LifeCognitionJob{}, invalidLifeJobOutput("decode life job output: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return db.LifeCognitionJob{}, invalidLifeJobOutput("life job output must contain one JSON object")
	}
	if err := validateLifeJobOutput(scope.job.JobType, output); err != nil {
		return db.LifeCognitionJob{}, err
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.LifeCognitionJob{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.Queries.WithTx(tx)

	createdMemories := make([]pgtype.UUID, 0, len(output.MemoryCandidates))
	for _, candidate := range output.MemoryCandidates {
		if !validLifeMemoryKind(candidate.Kind) || strings.TrimSpace(candidate.Content) == "" || candidate.Confidence < 0 || candidate.Confidence > 1 || candidate.Urgency < 0 || candidate.Urgency > 1 {
			return db.LifeCognitionJob{}, fmt.Errorf("invalid memory candidate in life job output")
		}
		forgotten, err := lifeEvidenceWasForgotten(ctx, q, scope, candidate.Evidence)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if forgotten {
			continue
		}
		memory, err := q.CreateLifeMemory(ctx, db.CreateLifeMemoryParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID,
			CreatedByType: "agent", CreatedByID: scope.agentID,
			Kind: candidate.Kind, Content: strings.TrimSpace(candidate.Content),
			Confidence: candidate.Confidence, Urgency: candidate.Urgency,
			Uncertainty: strings.TrimSpace(candidate.Uncertainty),
		})
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		createdMemories = append(createdMemories, memory.ID)
		if _, err := q.CreateLifeMemoryRevision(ctx, db.CreateLifeMemoryRevisionParams{
			MemoryID: memory.ID, Revision: 1, Kind: memory.Kind, Status: memory.Status,
			Content: memory.Content, Confidence: memory.Confidence, Urgency: memory.Urgency,
			Uncertainty: memory.Uncertainty, Scope: memory.Scope, ChangeType: "created",
			ChangeReason: "background cognition", ChangedByType: "agent", ChangedByID: scope.agentID,
		}); err != nil {
			return db.LifeCognitionJob{}, err
		}
		for _, evidence := range candidate.Evidence {
			sourceID, err := util.ParseUUID(evidence.SourceID)
			if err != nil || !validLifeEvidenceSourceType(evidence.SourceType) {
				return db.LifeCognitionJob{}, fmt.Errorf("invalid memory evidence")
			}
			observedAt, err := parseLifeJobTime(evidence.ObservedAt, time.Now())
			if err != nil {
				return db.LifeCognitionJob{}, err
			}
			stance := evidence.Stance
			if stance == "" {
				stance = "supports"
			}
			if stance != "supports" && stance != "contradicts" && stance != "context" {
				return db.LifeCognitionJob{}, fmt.Errorf("invalid memory evidence stance")
			}
			if _, err := q.CreateLifeMemoryEvidence(ctx, db.CreateLifeMemoryEvidenceParams{
				MemoryID: memory.ID, SourceType: evidence.SourceType, SourceID: sourceID,
				Excerpt: strings.TrimSpace(evidence.Excerpt), ObservedAt: observedAt, Stance: stance,
			}); err != nil {
				return db.LifeCognitionJob{}, err
			}
		}
		if err := recordLifeDerivations(ctx, q, scope, "memory", memory.ID, candidate.Evidence); err != nil {
			return db.LifeCognitionJob{}, err
		}
	}

	for _, item := range output.Topics {
		forgotten, err := lifeEvidenceWasForgotten(ctx, q, scope, item.Evidence)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if forgotten {
			continue
		}
		if strings.TrimSpace(item.Title) == "" || item.Confidence < 0 || item.Confidence > 1 {
			return db.LifeCognitionJob{}, fmt.Errorf("invalid topic in life job output")
		}
		status := item.Status
		if status == "" {
			status = "candidate"
		}
		now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
		var topic db.LifeTopic
		if strings.TrimSpace(item.ID) == "" {
			topic, err = q.CreateLifeTopic(ctx, db.CreateLifeTopicParams{
				WorkspaceID: scope.workspaceID, UserID: scope.userID, Title: strings.TrimSpace(item.Title),
				Summary: strings.TrimSpace(item.Summary), Status: status, Confidence: item.Confidence,
				Uncertainty: strings.TrimSpace(item.Uncertainty), FirstObservedAt: now, LastObservedAt: now,
			})
		} else {
			topicID, parseErr := util.ParseUUID(item.ID)
			if parseErr != nil {
				return db.LifeCognitionJob{}, fmt.Errorf("invalid topic_id")
			}
			existing, loadErr := q.GetLifeTopicForUser(ctx, db.GetLifeTopicForUserParams{ID: topicID, WorkspaceID: scope.workspaceID, UserID: scope.userID})
			if loadErr != nil {
				return db.LifeCognitionJob{}, loadErr
			}
			topic, err = q.UpdateLifeTopic(ctx, db.UpdateLifeTopicParams{ID: topicID, WorkspaceID: scope.workspaceID, UserID: scope.userID, Title: strings.TrimSpace(item.Title), Summary: strings.TrimSpace(item.Summary), Status: status, Confidence: item.Confidence, Uncertainty: strings.TrimSpace(item.Uncertainty), LastObservedAt: now, LastReviewedAt: now, ReviewAfter: existing.ReviewAfter})
		}
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		for i, rawID := range item.MemoryIDs {
			memoryID, err := util.ParseUUID(rawID)
			if err != nil {
				continue
			}
			relation := "context"
			if i < len(item.Relations) && (item.Relations[i] == "supports" || item.Relations[i] == "contradicts" || item.Relations[i] == "context") {
				relation = item.Relations[i]
			}
			if err := q.LinkLifeTopicMemory(ctx, db.LinkLifeTopicMemoryParams{TopicID: topic.ID, MemoryID: memoryID, Relation: relation}); err != nil {
				return db.LifeCognitionJob{}, err
			}
		}
		if err := recordLifeDerivations(ctx, q, scope, "topic", topic.ID, item.Evidence); err != nil {
			return db.LifeCognitionJob{}, err
		}
		for _, rawID := range item.MemoryIDs {
			if memoryID, parseErr := util.ParseUUID(rawID); parseErr == nil {
				if err := q.RecordLifeDerivation(ctx, db.RecordLifeDerivationParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, SourceType: "memory", SourceID: rawID, TargetType: "topic", TargetID: topic.ID, JobID: scope.job.ID}); err != nil {
					return db.LifeCognitionJob{}, err
				}
				_ = memoryID
			}
		}
	}

	for _, item := range output.Commitments {
		forgotten, err := lifeEvidenceWasForgotten(ctx, q, scope, item.Evidence)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if forgotten {
			continue
		}
		if strings.TrimSpace(item.Content) == "" {
			continue
		}
		dueAt, err := parseLifeJobOptionalTime(item.DueAt)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		revisitAfter, err := parseLifeJobOptionalTime(item.RevisitAfter)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		sourceMemoryID := pgtype.UUID{}
		if item.SourceMemoryID != "" {
			sourceMemoryID, err = util.ParseUUID(item.SourceMemoryID)
			if err != nil {
				return db.LifeCognitionJob{}, err
			}
		}
		commitment, err := q.CreateLifeCommitment(ctx, db.CreateLifeCommitmentParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID, Content: strings.TrimSpace(item.Content),
			Status: "candidate", SourceMemoryID: sourceMemoryID, DueAt: dueAt, RevisitAfter: revisitAfter,
		})
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if err := recordLifeDerivations(ctx, q, scope, "commitment", commitment.ID, item.Evidence); err != nil {
			return db.LifeCognitionJob{}, err
		}
		if sourceMemoryID.Valid {
			if err := q.RecordLifeDerivation(ctx, db.RecordLifeDerivationParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, SourceType: "memory", SourceID: util.UUIDToString(sourceMemoryID), TargetType: "commitment", TargetID: commitment.ID, JobID: scope.job.ID}); err != nil {
				return db.LifeCognitionJob{}, err
			}
		}
	}

	for _, item := range output.InternalThoughts {
		forgotten, err := lifeEvidenceWasForgotten(ctx, q, scope, item.Evidence)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if forgotten {
			continue
		}
		if strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Content) == "" {
			continue
		}
		metadata, _ := json.Marshal(item.Metadata)
		thought, err := q.UpsertLifeInternalThought(ctx, db.UpsertLifeInternalThoughtParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID, CompanionAgentID: scope.agentID,
			ThoughtType: item.Type, Title: strings.TrimSpace(item.Title), Content: strings.TrimSpace(item.Content),
			Status: "active", Metadata: metadata,
		})
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if err := recordLifeDerivations(ctx, q, scope, "internal_thought", thought.ID, item.Evidence); err != nil {
			return db.LifeCognitionJob{}, err
		}
	}

	for _, item := range output.RelationshipEvents {
		forgotten, err := lifeEvidenceWasForgotten(ctx, q, scope, item.Evidence)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if forgotten {
			continue
		}
		revisitAfter, err := parseLifeJobOptionalTime(item.RevisitAfter)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		status := item.Status
		if status == "" {
			status = "open"
		}
		event, err := q.CreateLifeRelationshipEvent(ctx, db.CreateLifeRelationshipEventParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID, EventType: item.Type, Status: status,
			UserPosition: item.UserPosition, CompanionPosition: item.CompanionPosition,
			Context: item.Context, RevisitAfter: revisitAfter,
		})
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if err := recordLifeDerivations(ctx, q, scope, "relationship_event", event.ID, item.Evidence); err != nil {
			return db.LifeCognitionJob{}, err
		}
	}

	for _, item := range output.ActionProposals {
		forgotten, err := lifeEvidenceWasForgotten(ctx, q, scope, item.Evidence)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if forgotten {
			continue
		}
		if item.ProposalType != "experiment_start" && item.ProposalType != "experiment_extend" && item.ProposalType != "workspace_issue" && item.ProposalType != "agent_action" && item.ProposalType != "project_create" && item.ProposalType != "module_adoption" && item.ProposalType != "memory_change" && item.ProposalType != "identity_change" {
			return db.LifeCognitionJob{}, fmt.Errorf("invalid action proposal type")
		}
		if strings.TrimSpace(item.Title) == "" || item.Payload == nil {
			return db.LifeCognitionJob{}, fmt.Errorf("action proposal title and payload are required")
		}
		payload, err := json.Marshal(item.Payload)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		expiresAt, err := parseLifeJobOptionalTime(item.ExpiresAt)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		proposal, err := q.CreateLifeActionProposal(ctx, db.CreateLifeActionProposalParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID, CompanionAgentID: scope.agentID,
			ProposalType: item.ProposalType, Status: "pending_confirmation", Title: strings.TrimSpace(item.Title),
			Summary: strings.TrimSpace(item.Summary), Payload: payload, ExpiresAt: expiresAt,
		})
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if err := recordLifeDerivations(ctx, q, scope, "action_proposal", proposal.ID, item.Evidence); err != nil {
			return db.LifeCognitionJob{}, err
		}
	}

	if decision := output.ProactiveDecision; decision != nil {
		forgotten, err := lifeEvidenceWasForgotten(ctx, q, scope, decision.Evidence)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if forgotten {
			decision = nil
		}
		if decision == nil {
			goto proactiveDone
		}
		if decision.Status != "silent" && decision.Status != "spoke" {
			return db.LifeCognitionJob{}, fmt.Errorf("invalid proactive decision status")
		}
		if decision.TriggerSource != "schedule" && decision.TriggerSource != "commitment" && decision.TriggerSource != "risk" && decision.TriggerSource != "manual" {
			return db.LifeCognitionJob{}, fmt.Errorf("invalid proactive trigger source")
		}
		contextSnapshot, _ := json.Marshal(decision.ContextSnapshot)
		check, err := q.CreateLifeProactiveCheckFull(ctx, db.CreateLifeProactiveCheckFullParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID, CompanionAgentID: scope.agentID,
			Status: decision.Status, TriggerSource: decision.TriggerSource,
			Reason: strings.TrimSpace(decision.Reason), ContextSnapshot: contextSnapshot,
			Message: strings.TrimSpace(decision.Message),
		})
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if err := recordLifeDerivations(ctx, q, scope, "proactive_check", check.ID, decision.Evidence); err != nil {
			return db.LifeCognitionJob{}, err
		}
		if decision.Status == "spoke" && strings.TrimSpace(decision.Message) != "" {
			details, _ := json.Marshal(map[string]any{"life_proactive_check_id": util.UUIDToString(check.ID), "path": "/companion"})
			if _, err := q.CreateInboxItem(ctx, db.CreateInboxItemParams{
				WorkspaceID: scope.workspaceID, RecipientType: "member", RecipientID: scope.userID,
				Type: "life_companion", Severity: "info", Title: "搭子想和你聊聊",
				Body:      pgtype.Text{String: strings.TrimSpace(decision.Message), Valid: true},
				ActorType: pgtype.Text{String: "agent", Valid: true}, ActorID: scope.agentID, Details: details,
			}); err != nil {
				return db.LifeCognitionJob{}, err
			}
			if err := q.RecordLifeProactiveSpeech(ctx, db.RecordLifeProactiveSpeechParams{WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
				return db.LifeCognitionJob{}, err
			}
		}
	}
proactiveDone:
	if assessment := output.ProactiveAssessment; assessment != nil {
		checkID, err := util.ParseUUID(assessment.CheckID)
		if err != nil || strings.TrimSpace(assessment.ValueAssessment) == "" {
			return db.LifeCognitionJob{}, fmt.Errorf("invalid proactive assessment")
		}
		if _, err := q.RecordLifeProactiveAssessment(ctx, db.RecordLifeProactiveAssessmentParams{
			ID: checkID, WorkspaceID: scope.workspaceID, UserID: scope.userID,
			ValueAssessment: strings.TrimSpace(assessment.ValueAssessment),
		}); err != nil {
			return db.LifeCognitionJob{}, err
		}
		if assessment.MinimumIntervalHours >= 1 && assessment.MinimumIntervalHours <= 168 {
			if err := q.AdjustLifeProactiveInterval(ctx, db.AdjustLifeProactiveIntervalParams{
				WorkspaceID: scope.workspaceID, UserID: scope.userID, Hours: assessment.MinimumIntervalHours,
			}); err != nil {
				return db.LifeCognitionJob{}, err
			}
		}
	}

	for _, item := range output.ExperimentObservations {
		roundID, err := util.ParseUUID(item.RoundID)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if _, err := q.GetLifeExperimentRoundForUser(ctx, db.GetLifeExperimentRoundForUserParams{
			ID: roundID, WorkspaceID: scope.workspaceID, UserID: scope.userID,
		}); err != nil {
			return db.LifeCognitionJob{}, err
		}
		materialID := pgtype.UUID{}
		if item.MaterialID != "" {
			materialID, err = util.ParseUUID(item.MaterialID)
			if err != nil {
				return db.LifeCognitionJob{}, err
			}
			material, materialErr := q.GetLifeMaterialForUser(ctx, db.GetLifeMaterialForUserParams{ID: materialID, WorkspaceID: scope.workspaceID, UserID: scope.userID})
			if errors.Is(materialErr, pgx.ErrNoRows) {
				continue
			}
			if materialErr != nil {
				return db.LifeCognitionJob{}, materialErr
			}
			forgotten, checkErr := lifeEvidenceWasForgotten(ctx, q, scope, []lifeJobEvidenceOutput{{SourceType: material.SourceType, SourceID: item.MaterialID}})
			if checkErr != nil {
				return db.LifeCognitionJob{}, checkErr
			}
			if forgotten {
				continue
			}
		}
		observedAt, err := parseLifeJobTime(item.ObservedAt, time.Now())
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		observation, err := q.CreateLifeExperimentObservation(ctx, db.CreateLifeExperimentObservationParams{
			RoundID: roundID, MaterialID: materialID, ObservationType: item.Type,
			Content: strings.TrimSpace(item.Content), CapturedBy: "companion", ObservedAt: observedAt,
		})
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if materialID.Valid {
			if err := q.RecordLifeDerivation(ctx, db.RecordLifeDerivationParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, SourceType: "material", SourceID: util.UUIDToString(materialID), TargetType: "experiment_observation", TargetID: observation.ID, JobID: scope.job.ID}); err != nil {
				return db.LifeCognitionJob{}, err
			}
		}
	}

	if review := output.ExperimentReview; review != nil {
		forgotten, err := lifeEvidenceWasForgotten(ctx, q, scope, review.Evidence)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if forgotten {
			review = nil
		}
		if review == nil {
			goto experimentReviewDone
		}
		roundID, err := util.ParseUUID(review.RoundID)
		if err != nil {
			return db.LifeCognitionJob{}, fmt.Errorf("invalid experiment review round")
		}
		draft, err := json.Marshal(map[string]any{
			"outcome": strings.TrimSpace(review.Outcome), "feelings": strings.TrimSpace(review.Feelings),
			"burden": strings.TrimSpace(review.Burden), "companion_correction": strings.TrimSpace(review.CompanionCorrection),
			"module_proposal": review.ModuleProposal,
		})
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if _, err := q.SaveLifeExperimentReviewDraft(ctx, db.SaveLifeExperimentReviewDraftParams{ID: roundID, WorkspaceID: scope.workspaceID, UserID: scope.userID, ReviewDraft: draft}); err != nil {
			return db.LifeCognitionJob{}, err
		}
		if err := recordLifeDerivations(ctx, q, scope, "experiment_round_review", roundID, review.Evidence); err != nil {
			return db.LifeCognitionJob{}, err
		}
	}
experimentReviewDone:

	if len(output.ObserverJudgements) > 0 {
		observer, err := q.GetLifeObserverForAgent(ctx, db.GetLifeObserverForAgentParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID, AgentID: scope.agentID,
		})
		if err != nil {
			return db.LifeCognitionJob{}, fmt.Errorf("observer judgement requires observer task: %w", err)
		}
		publishedIDs := make([]string, 0, len(output.ObserverJudgements))
		for _, item := range output.ObserverJudgements {
			forgotten, err := lifeEvidenceWasForgotten(ctx, q, scope, item.Evidence)
			if err != nil {
				return db.LifeCognitionJob{}, err
			}
			if forgotten {
				continue
			}
			evidence, _ := json.Marshal(item.Evidence)
			publishedAt := pgtype.Timestamptz{}
			if item.Status == "published" {
				publishedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
			}
			judgement, err := q.CreateLifeObserverJudgement(ctx, db.CreateLifeObserverJudgementParams{
				ObserverID: observer.ID, Status: item.Status, Title: strings.TrimSpace(item.Title),
				Content: strings.TrimSpace(item.Content), Evidence: evidence,
				Confidence: item.Confidence, Uncertainty: strings.TrimSpace(item.Uncertainty), PublishedAt: publishedAt,
			})
			if err != nil {
				return db.LifeCognitionJob{}, err
			}
			if err := recordLifeDerivations(ctx, q, scope, "observer_judgement", judgement.ID, item.Evidence); err != nil {
				return db.LifeCognitionJob{}, err
			}
			if item.Status == "published" {
				publishedIDs = append(publishedIDs, util.UUIDToString(judgement.ID))
			}
		}
		if len(publishedIDs) > 0 {
			profile, err := q.GetCompanionProfile(ctx, db.GetCompanionProfileParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
			if err != nil {
				return db.LifeCognitionJob{}, err
			}
			input, _ := json.Marshal(map[string]any{"new_judgement_ids": publishedIDs})
			if _, err := q.CreateLifeCognitionJob(ctx, db.CreateLifeCognitionJobParams{
				WorkspaceID: scope.workspaceID, UserID: scope.userID, CompanionAgentID: profile.AgentID,
				JobType: "observation_aggregate", DedupeKey: "observer-job:" + util.UUIDToString(scope.job.ID), Input: input,
				ScheduledAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
			}); err != nil {
				return db.LifeCognitionJob{}, err
			}
		}
	}

	for _, item := range output.ObservationTopics {
		if strings.TrimSpace(item.Title) == "" || len(item.JudgementIDs) == 0 {
			continue
		}
		status := item.Status
		if status == "" {
			status = "open"
		}
		surfacedAt := pgtype.Timestamptz{}
		if status == "surfaced" || status == "discussing" {
			surfacedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		}
		var topic db.LifeObservationTopic
		var err error
		if strings.TrimSpace(item.ID) == "" {
			topic, err = q.CreateLifeObservationTopic(ctx, db.CreateLifeObservationTopicParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Title: strings.TrimSpace(item.Title), Summary: strings.TrimSpace(item.Summary), Status: status, SurfacedAt: surfacedAt})
		} else {
			topicID, parseErr := util.ParseUUID(item.ID)
			if parseErr != nil {
				return db.LifeCognitionJob{}, fmt.Errorf("invalid observation topic_id")
			}
			topic, err = q.MergeLifeObservationTopic(ctx, db.MergeLifeObservationTopicParams{ID: topicID, WorkspaceID: scope.workspaceID, UserID: scope.userID, Title: strings.TrimSpace(item.Title), Summary: strings.TrimSpace(item.Summary), Status: status})
		}
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		for _, rawID := range item.JudgementIDs {
			id, err := util.ParseUUID(rawID)
			if err != nil {
				return db.LifeCognitionJob{}, fmt.Errorf("invalid observer judgement id")
			}
			if err := q.LinkLifeObservationTopicJudgement(ctx, db.LinkLifeObservationTopicJudgementParams{TopicID: topic.ID, JudgementID: id}); err != nil {
				return db.LifeCognitionJob{}, err
			}
			if err := q.RecordLifeDerivation(ctx, db.RecordLifeDerivationParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, SourceType: "observer_judgement", SourceID: rawID, TargetType: "observation_topic", TargetID: topic.ID, JobID: scope.job.ID}); err != nil {
				return db.LifeCognitionJob{}, err
			}
		}
	}

	for _, item := range output.Chronicles {
		forgotten, err := lifeEvidenceWasForgotten(ctx, q, scope, item.Evidence)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if forgotten {
			continue
		}
		periodStart, err := parseLifeJobTime(item.PeriodStart, time.Time{})
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		periodEnd, err := parseLifeJobTime(item.PeriodEnd, time.Time{})
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		entry, err := q.CreateGeneratedLifeChronicleEntry(ctx, db.CreateGeneratedLifeChronicleEntryParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID,
			PeriodStart: periodStart, PeriodEnd: periodEnd, PeriodKind: item.PeriodKind,
			Facts: strings.TrimSpace(item.Facts), Feelings: strings.TrimSpace(item.Feelings),
			UnderstandingThen:  strings.TrimSpace(item.UnderstandingThen),
			UnderstandingLater: strings.TrimSpace(item.UnderstandingLater), GeneratedBy: "companion",
			Actions: strings.TrimSpace(item.Actions),
		})
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if _, err := q.CreateLifeChronicleRevision(ctx, db.CreateLifeChronicleRevisionParams{
			EntryID: entry.ID, Revision: entry.Revision, Facts: entry.Facts, Feelings: entry.Feelings,
			UnderstandingThen: entry.UnderstandingThen, UnderstandingLater: entry.UnderstandingLater,
			Actions:      entry.Actions,
			ChangeReason: "后台生成",
		}); err != nil {
			return db.LifeCognitionJob{}, err
		}
		for _, evidence := range item.Evidence {
			sourceID, err := util.ParseUUID(evidence.SourceID)
			if err != nil {
				return db.LifeCognitionJob{}, err
			}
			if err := q.CreateLifeChronicleEvidenceLink(ctx, db.CreateLifeChronicleEvidenceLinkParams{
				EntryID: entry.ID, SourceType: evidence.SourceType, SourceID: sourceID,
			}); err != nil {
				return db.LifeCognitionJob{}, err
			}
		}
		if err := recordLifeDerivations(ctx, q, scope, "chronicle_entry", entry.ID, item.Evidence); err != nil {
			return db.LifeCognitionJob{}, err
		}
	}

	if evaluation := output.UpgradeEvaluation; evaluation != nil {
		forgotten, err := lifeEvidenceWasForgotten(ctx, q, scope, evaluation.Evidence)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if forgotten {
			evaluation = nil
		}
		if evaluation == nil {
			goto upgradeEvaluationDone
		}
		id, err := util.ParseUUID(evaluation.EvaluationID)
		if err != nil {
			return db.LifeCognitionJob{}, fmt.Errorf("invalid upgrade evaluation id")
		}
		if evaluation.Status != "passed" && evaluation.Status != "failed" && evaluation.Status != "unknown" {
			return db.LifeCognitionJob{}, fmt.Errorf("invalid upgrade evaluation status")
		}
		result, err := json.Marshal(evaluation.Result)
		if err != nil {
			return db.LifeCognitionJob{}, err
		}
		if _, err := q.CompleteLifeUpgradeEvaluation(ctx, db.CompleteLifeUpgradeEvaluationParams{ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID, Status: evaluation.Status, Result: result, RollbackRecommended: evaluation.RollbackRecommended}); err != nil {
			return db.LifeCognitionJob{}, err
		}
		if err := recordLifeDerivations(ctx, q, scope, "upgrade_evaluation", id, evaluation.Evidence); err != nil {
			return db.LifeCognitionJob{}, err
		}
	}
upgradeEvaluationDone:

	completed, err := q.CompleteLifeCognitionJob(ctx, db.CompleteLifeCognitionJobParams{
		ID: scope.job.ID, WorkspaceID: scope.workspaceID, UserID: scope.userID, Output: raw,
	})
	if err != nil {
		return db.LifeCognitionJob{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.LifeCognitionJob{}, err
	}
	_ = createdMemories
	return completed, nil
}

func parseLifeJobOptionalTime(raw string) (pgtype.Timestamptz, error) {
	if strings.TrimSpace(raw) == "" {
		return pgtype.Timestamptz{}, nil
	}
	return parseLifeJobTime(raw, time.Time{})
}

func parseLifeJobTime(raw string, fallback time.Time) (pgtype.Timestamptz, error) {
	if strings.TrimSpace(raw) == "" {
		if fallback.IsZero() {
			return pgtype.Timestamptz{}, fmt.Errorf("time is required")
		}
		return pgtype.Timestamptz{Time: fallback, Valid: true}, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return pgtype.Timestamptz{}, fmt.Errorf("invalid RFC3339 time %q", raw)
	}
	return pgtype.Timestamptz{Time: parsed, Valid: true}, nil
}
