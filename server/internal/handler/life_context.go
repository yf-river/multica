package handler

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	lifeContextVersion              = "life-context-v5"
	confirmedLifeMemoryIndexLimit   = 24
	candidateLifeMemoryIndexLimit   = 8
	lifeTopicIndexLimit             = 12
	lifeCommitmentIndexLimit        = 12
	activeLifeModuleIndexLimit      = 8
	activeLifeExperimentIndexLimit  = 8
	recentLifeMaterialIndexLimit    = 8
	lifeRelationshipEventIndexLimit = 8
	lifeInternalThoughtIndexLimit   = 8
	lifeObserverKnowledgeIndexLimit = 16
	lifeObserverJudgementIndexLimit = 12
	lifeObservationTopicIndexLimit  = 12
	lifeYearChronicleIndexLimit     = 10
	lifeMonthChronicleIndexLimit    = 4
	lifeEventChronicleIndexLimit    = 4
)

// buildGovernedLifeContext assembles only governed shared context. Internal
// observer judgements and private companion thoughts are intentionally kept
// out of the common context until their own publication rules expose them.
func (h *Handler) buildGovernedLifeContext(ctx context.Context, scope lifeRequestScope) (string, error) {
	result := map[string]any{"context_version": lifeContextVersion}
	profile, err := h.Queries.GetCompanionProfile(ctx, db.GetCompanionProfileParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID,
	})
	if err == nil && len(profile.ReturnContext) > 0 && string(profile.ReturnContext) != "{}" {
		result["return_context"] = json.RawMessage(profile.ReturnContext)
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	identity, err := h.Queries.GetActiveLifeIdentity(ctx, db.GetActiveLifeIdentityParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID,
	})
	if err == nil {
		result["identity"] = map[string]any{
			"version":               identity.Version,
			"stable_core":           json.RawMessage(identity.StableCore),
			"relationship_contract": json.RawMessage(identity.RelationshipContract),
			"growth_profile":        json.RawMessage(identity.GrowthProfile),
			"expression_profile":    json.RawMessage(identity.ExpressionProfile),
			"interests":             json.RawMessage(identity.Interests),
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	memories, err := h.Queries.ListConfirmedLifeMemoriesForContext(ctx, db.ListConfirmedLifeMemoriesForContextParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, Limit: confirmedLifeMemoryIndexLimit,
	})
	if err != nil {
		return "", err
	}
	memoryItems := make([]map[string]any, 0, len(memories))
	for _, memory := range memories {
		memoryItems = append(memoryItems, map[string]any{
			"id": uuidToString(memory.ID), "kind": memory.Kind,
			"content": lifeContextExcerpt(memory.Content, 80), "confidence": memory.Confidence,
			"uncertainty": lifeContextExcerpt(memory.Uncertainty, 40), "scope": json.RawMessage(memory.Scope),
			"valid_from": timestampToPtr(memory.ValidFrom), "valid_to": timestampToPtr(memory.ValidTo),
		})
	}
	result["confirmed_memories"] = memoryItems
	candidates, err := h.Queries.ListLifeMemoriesByStatus(ctx, db.ListLifeMemoriesByStatusParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Status: "candidate"})
	if err != nil {
		return "", err
	}
	candidateItems := make([]map[string]any, 0, min(len(candidates), candidateLifeMemoryIndexLimit))
	for _, memory := range capLifeContextItems(candidates, candidateLifeMemoryIndexLimit) {
		candidateItems = append(candidateItems, map[string]any{"id": uuidToString(memory.ID), "kind": memory.Kind, "content": lifeContextExcerpt(memory.Content, 60), "confidence": memory.Confidence, "urgency": memory.Urgency, "uncertainty": lifeContextExcerpt(memory.Uncertainty, 40), "review_after": timestampToPtr(memory.ReviewAfter)})
	}
	result["candidate_memories_not_facts"] = candidateItems

	topics, err := h.Queries.ListLifeTopics(ctx, db.ListLifeTopicsParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, Status: pgtype.Text{},
	})
	if err != nil {
		return "", err
	}
	topicItems := make([]map[string]any, 0, min(len(topics), lifeTopicIndexLimit))
	for _, topic := range topics {
		if topic.Status == "archived" || topic.Status == "resolved" {
			continue
		}
		if len(topicItems) >= lifeTopicIndexLimit {
			break
		}
		topicItems = append(topicItems, map[string]any{
			"id": uuidToString(topic.ID), "title": lifeContextExcerpt(topic.Title, 60), "summary": lifeContextExcerpt(topic.Summary, 80),
			"status": topic.Status, "confidence": topic.Confidence,
			"uncertainty": lifeContextExcerpt(topic.Uncertainty, 40), "last_observed_at": timestampToString(topic.LastObservedAt),
		})
	}
	result["current_topics"] = topicItems

	commitments, err := h.Queries.ListLifeCommitments(ctx, db.ListLifeCommitmentsParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, Status: pgtype.Text{},
	})
	if err != nil {
		return "", err
	}
	commitmentItems := make([]map[string]any, 0, min(len(commitments), lifeCommitmentIndexLimit))
	for _, commitment := range commitments {
		if commitment.Status != "confirmed" {
			continue
		}
		if len(commitmentItems) >= lifeCommitmentIndexLimit {
			break
		}
		commitmentItems = append(commitmentItems, map[string]any{
			"id": uuidToString(commitment.ID), "content": lifeContextExcerpt(commitment.Content, 80),
			"due_at": timestampToPtr(commitment.DueAt), "revisit_after": timestampToPtr(commitment.RevisitAfter),
		})
	}
	result["confirmed_commitments"] = commitmentItems

	modules, err := h.Queries.ListLifeModules(ctx, db.ListLifeModulesParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		return "", err
	}
	moduleItems := make([]map[string]any, 0, len(modules))
	for _, module := range modules {
		if module.Status != "active" {
			continue
		}
		if len(moduleItems) >= activeLifeModuleIndexLimit {
			break
		}
		versions, err := h.Queries.ListLifeModuleVersions(ctx, module.ID)
		if err != nil {
			return "", err
		}
		for _, version := range versions {
			if version.Version == module.CurrentVersion {
				moduleItems = append(moduleItems, map[string]any{"id": uuidToString(module.ID), "name": lifeContextExcerpt(module.Name, 60), "version": version.Version, "definition_excerpt": lifeContextExcerpt(string(version.Definition), 120)})
				break
			}
		}
	}
	result["active_life_modules"] = moduleItems

	experiments, err := h.Queries.ListLifeExperiments(ctx, db.ListLifeExperimentsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		return "", err
	}
	rounds, err := h.Queries.ListLifeExperimentRounds(ctx, db.ListLifeExperimentRoundsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
	if err != nil {
		return "", err
	}
	experimentByID := make(map[string]db.LifeExperiment, len(experiments))
	for _, experiment := range experiments {
		experimentByID[uuidToString(experiment.ID)] = experiment
	}
	experimentItems := make([]map[string]any, 0)
	for _, round := range rounds {
		if round.Status != "running" && round.Status != "awaiting_review" {
			continue
		}
		if len(experimentItems) >= activeLifeExperimentIndexLimit {
			break
		}
		experiment := experimentByID[uuidToString(round.ExperimentID)]
		experimentItems = append(experimentItems, map[string]any{"experiment_id": uuidToString(round.ExperimentID), "round_id": uuidToString(round.ID), "title": lifeContextExcerpt(experiment.Title, 60), "problem": lifeContextExcerpt(experiment.Problem, 80), "hypothesis": lifeContextExcerpt(experiment.Hypothesis, 80), "status": round.Status, "plan_excerpt": lifeContextExcerpt(string(round.Plan), 120), "ends_at": timestampToPtr(round.EndsAt), "review_excerpt": lifeContextExcerpt(string(round.ReviewDraft), 120)})
	}
	result["active_experiments"] = experimentItems

	materials, err := h.Queries.ListLifeMaterials(ctx, db.ListLifeMaterialsParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, Limit: recentLifeMaterialIndexLimit,
	})
	if err != nil {
		return "", err
	}
	materialItems := make([]map[string]any, 0, len(materials))
	for _, material := range materials {
		materialItems = append(materialItems, map[string]any{
			"id": uuidToString(material.ID), "source_type": material.SourceType,
			"occurred_at": timestampToString(material.OccurredAt),
			"excerpt":     lifeContextExcerpt(material.Content, 60),
		})
	}
	result["recent_material_index"] = materialItems

	chronicles, err := h.Queries.ListLifeChronicleContextEntries(ctx, db.ListLifeChronicleContextEntriesParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID,
	})
	if err != nil {
		return "", err
	}
	chronicleItems := make([]map[string]any, 0, min(len(chronicles), lifeYearChronicleIndexLimit+lifeMonthChronicleIndexLimit+lifeEventChronicleIndexLimit))
	chronicleCounts := map[string]int{}
	for _, entry := range chronicles {
		limit := 0
		switch entry.PeriodKind {
		case "year":
			limit = lifeYearChronicleIndexLimit
		case "month":
			limit = lifeMonthChronicleIndexLimit
		case "event":
			limit = lifeEventChronicleIndexLimit
		}
		if limit == 0 {
			continue
		}
		if chronicleCounts[entry.PeriodKind] >= limit {
			continue
		}
		chronicleCounts[entry.PeriodKind]++
		chronicleItems = append(chronicleItems, map[string]any{
			"id": uuidToString(entry.ID), "period_kind": entry.PeriodKind,
			"period_start": timestampToString(entry.PeriodStart), "period_end": timestampToString(entry.PeriodEnd),
			"facts": lifeContextExcerpt(entry.Facts, 60), "understanding_later": lifeContextExcerpt(entry.UnderstandingLater, 60),
		})
	}
	result["chronicle_index"] = chronicleItems

	events, err := h.Queries.ListLifeRelationshipEvents(ctx, db.ListLifeRelationshipEventsParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID,
	})
	if err != nil {
		return "", err
	}
	eventItems := make([]map[string]any, 0, min(len(events), lifeRelationshipEventIndexLimit))
	for _, event := range events {
		if event.Status == "resolved" {
			continue
		}
		if len(eventItems) >= lifeRelationshipEventIndexLimit {
			break
		}
		eventItems = append(eventItems, map[string]any{
			"id": uuidToString(event.ID), "type": event.EventType, "status": event.Status,
			"user_position": lifeContextExcerpt(event.UserPosition, 60), "companion_position": lifeContextExcerpt(event.CompanionPosition, 60),
			"context": lifeContextExcerpt(event.Context, 80), "revisit_after": timestampToPtr(event.RevisitAfter),
		})
	}
	result["open_relationship_events"] = eventItems

	raw, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func lifeContextExcerpt(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

func capLifeContextItems[T any](items []T, limit int) []T {
	if len(items) <= limit {
		return items
	}
	return items[:limit]
}

func currentLifeJobInput(input json.RawMessage) (json.RawMessage, error) {
	var value map[string]any
	if err := json.Unmarshal(input, &value); err != nil || value == nil {
		if err == nil {
			err = errors.New("life cognition input must be a JSON object")
		}
		return nil, err
	}
	value["context_version"] = lifeContextVersion
	return json.Marshal(value)
}

func (h *Handler) addLifeInternalThoughts(ctx context.Context, result map[string]any, scope lifeRequestScope, agentID pgtype.UUID) error {
	thoughts, err := h.Queries.ListLifeInternalThoughts(ctx, db.ListLifeInternalThoughtsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, CompanionAgentID: agentID, Status: pgtype.Text{String: "active", Valid: true}})
	if err != nil {
		return err
	}
	items := make([]map[string]any, 0, min(len(thoughts), lifeInternalThoughtIndexLimit))
	for _, thought := range capLifeContextItems(thoughts, lifeInternalThoughtIndexLimit) {
		items = append(items, map[string]any{"id": uuidToString(thought.ID), "type": thought.ThoughtType, "title": lifeContextExcerpt(thought.Title, 60), "content": lifeContextExcerpt(thought.Content, 80), "last_developed_at": timestampToString(thought.LastDevelopedAt)})
	}
	result["agent_internal_thoughts"] = items
	return nil
}

func (h *Handler) buildCompanionChatLifeContext(ctx context.Context, scope lifeRequestScope, agentID pgtype.UUID) (string, error) {
	governed, err := h.buildGovernedLifeContext(ctx, scope)
	if err != nil {
		return "", err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(governed), &result); err != nil {
		return "", err
	}
	if err := h.addLifeInternalThoughts(ctx, result, scope, agentID); err != nil {
		return "", err
	}
	raw, err := json.Marshal(result)
	return string(raw), err
}

func (h *Handler) buildLifeJobContext(ctx context.Context, scope lifeRequestScope, jobType string, agentID pgtype.UUID, jobInput json.RawMessage) (string, error) {
	governed, err := h.buildGovernedLifeContext(ctx, scope)
	if err != nil {
		return "", err
	}
	if jobType == "observation_aggregate" {
		var result map[string]any
		if err := json.Unmarshal([]byte(governed), &result); err != nil {
			return "", err
		}
		judgements, err := h.Queries.ListPublishedLifeObserverJudgements(ctx, db.ListPublishedLifeObserverJudgementsParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID,
		})
		if err != nil {
			return "", err
		}
		requested := lifeObservationJudgementIDs(jobInput)
		items := make([]map[string]any, 0, min(len(requested), lifeObserverJudgementIndexLimit))
		for _, item := range judgements {
			if _, ok := requested[uuidToString(item.ID)]; !ok {
				continue
			}
			if len(items) >= lifeObserverJudgementIndexLimit {
				break
			}
			items = append(items, map[string]any{
				"id": uuidToString(item.ID), "observer_name": lifeContextExcerpt(item.ObserverName, 60), "title": lifeContextExcerpt(item.Title, 80),
				"content":    lifeContextExcerpt(item.Content, 120),
				"confidence": item.Confidence, "uncertainty": lifeContextExcerpt(item.Uncertainty, 60),
			})
		}
		result["published_observer_judgements"] = items
		topics, err := h.Queries.ListLifeObservationTopics(ctx, db.ListLifeObservationTopicsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID})
		if err != nil {
			return "", err
		}
		topicItems := make([]map[string]any, 0, len(topics))
		for _, topic := range topics {
			if topic.Status == "archived" || topic.Status == "resolved" {
				continue
			}
			if len(topicItems) >= lifeObservationTopicIndexLimit {
				break
			}
			topicItems = append(topicItems, map[string]any{"id": uuidToString(topic.ID), "title": lifeContextExcerpt(topic.Title, 80), "summary": lifeContextExcerpt(topic.Summary, 120), "status": topic.Status})
		}
		result["existing_observation_topics"] = topicItems
		if err := h.addLifeInternalThoughts(ctx, result, scope, agentID); err != nil {
			return "", err
		}
		raw, err := json.Marshal(result)
		return string(raw), err
	}
	if jobType != "observer_run" {
		var result map[string]any
		if err := json.Unmarshal([]byte(governed), &result); err != nil {
			return "", err
		}
		if err := h.addLifeInternalThoughts(ctx, result, scope, agentID); err != nil {
			return "", err
		}
		raw, err := json.Marshal(result)
		return string(raw), err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(governed), &result); err != nil {
		return "", err
	}
	observer, err := h.Queries.GetLifeObserverForAgent(ctx, db.GetLifeObserverForAgentParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, AgentID: agentID,
	})
	if err != nil {
		return "", err
	}
	version, err := h.Queries.GetCurrentLifeObserverVersion(ctx, observer.ID)
	if err != nil {
		return "", err
	}
	knowledge, err := h.Queries.ListLifeObserverKnowledge(ctx, observer.ID)
	if err != nil {
		return "", err
	}
	items := make([]map[string]any, 0, min(len(knowledge), lifeObserverKnowledgeIndexLimit))
	for _, item := range capLifeContextItems(knowledge, lifeObserverKnowledgeIndexLimit) {
		items = append(items, map[string]any{
			"id": uuidToString(item.ID), "title": lifeContextExcerpt(item.Title, 80), "excerpt": lifeContextExcerpt(item.Content, 120), "source": item.Source,
		})
	}
	result["observer_identity"] = map[string]any{
		"id": uuidToString(observer.ID), "name": observer.Name, "basis_type": observer.BasisType,
		"personality": json.RawMessage(version.Personality), "perspective": json.RawMessage(version.Perspective),
		"expression_profile": json.RawMessage(version.ExpressionProfile), "knowledge": items,
	}
	if err := h.addLifeInternalThoughts(ctx, result, scope, agentID); err != nil {
		return "", err
	}
	raw, err := json.Marshal(result)
	return string(raw), err
}

func lifeObservationJudgementIDs(input json.RawMessage) map[string]struct{} {
	var value struct {
		IDs []string `json:"new_judgement_ids"`
	}
	if json.Unmarshal(input, &value) != nil {
		return map[string]struct{}{}
	}
	result := make(map[string]struct{}, len(value.IDs))
	for _, id := range value.IDs {
		if _, err := util.ParseUUID(id); err == nil {
			result[id] = struct{}{}
		}
	}
	return result
}
