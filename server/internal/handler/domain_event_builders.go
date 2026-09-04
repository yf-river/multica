package handler

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// buildProjectDomainEvent creates the durable envelope for a project mutation.
// Projects do not carry a revision column, so UpdatedAt is the stable version
// component for updates; the database row id remains the aggregate stream.
func buildProjectDomainEvent(eventType string, project db.Project, actorType, actorID string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["project_id"] = util.UUIDToString(project.ID)
	if eventType != protocol.EventProjectDeleted {
		payload["project"] = projectToResponse(project)
	}
	version := strconv.FormatInt(project.UpdatedAt.Time.UnixNano(), 10)
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "project:" + eventType + ":" + util.UUIDToString(project.ID) + ":" + version,
		StreamKey:      "project:" + util.UUIDToString(project.ID),
		WorkspaceID:    util.UUIDToString(project.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

func buildProjectResourceDomainEvent(eventType string, resource db.ProjectResource, actorType, actorID string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["project_id"] = util.UUIDToString(resource.ProjectID)
	payload["resource_id"] = util.UUIDToString(resource.ID)
	payload["resource"] = projectResourceToResponse(resource)
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "project_resource:" + eventType + ":" + util.UUIDToString(resource.ID) + ":" + strconv.FormatInt(resource.Revision, 10),
		StreamKey:      "project:" + util.UUIDToString(resource.ProjectID),
		WorkspaceID:    util.UUIDToString(resource.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

func buildLabelDomainEvent(eventType string, label db.IssueLabel, actorType, actorID string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["label_id"] = util.UUIDToString(label.ID)
	payload["label"] = labelToResponse(label)
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "label:" + eventType + ":" + util.UUIDToString(label.ID) + ":" + label.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "workspace:" + util.UUIDToString(label.WorkspaceID),
		WorkspaceID:    util.UUIDToString(label.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

func buildIssueLabelsChangedEvent(issue db.Issue, actorType, actorID string, labels []LabelResponse, revision int64) events.Event {
	return events.Event{
		Type:           protocol.EventIssueLabelsChanged,
		IdempotencyKey: "issue_labels:changed:" + util.UUIDToString(issue.ID) + ":" + strconv.FormatInt(revision, 10),
		StreamKey:      "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID:    util.UUIDToString(issue.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"issue_id":       util.UUIDToString(issue.ID),
			"labels":         labels,
			"issue_revision": revision,
		},
	}
}

func buildPinDomainEvent(eventType, workspaceID, userID, actorType, actorID string, payload map[string]any, key string) events.Event {
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "pin:" + eventType + ":" + key,
		StreamKey:      "user:" + userID,
		WorkspaceID:    workspaceID,
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

func buildPropertyDomainEvent(eventType string, property db.IssueProperty, actorType, actorID string) events.Event {
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "property:" + eventType + ":" + util.UUIDToString(property.ID) + ":" + property.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "workspace:" + util.UUIDToString(property.WorkspaceID),
		WorkspaceID:    util.UUIDToString(property.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        map[string]any{"property": propertyToResponse(property, 0), "property_id": util.UUIDToString(property.ID)},
	}
}

func buildIssuePropertiesChangedEvent(issue db.Issue, actorType, actorID string, properties map[string]any) events.Event {
	return events.Event{
		Type:           protocol.EventIssuePropertiesChanged,
		IdempotencyKey: "issue_properties:changed:" + util.UUIDToString(issue.ID) + ":" + strconv.FormatInt(issue.Revision, 10),
		StreamKey:      "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID:    util.UUIDToString(issue.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        map[string]any{"issue_id": util.UUIDToString(issue.ID), "properties": properties, "issue_revision": issue.Revision},
	}
}

func buildSquadDomainEvent(eventType string, squad db.Squad, actorType, actorID string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["squad_id"] = util.UUIDToString(squad.ID)
	if eventType != protocol.EventSquadDeleted {
		payload["squad"] = map[string]any{
			"id": util.UUIDToString(squad.ID), "workspace_id": util.UUIDToString(squad.WorkspaceID),
			"name": squad.Name, "leader_id": util.UUIDToString(squad.LeaderID),
		}
	}
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "squad:" + eventType + ":" + util.UUIDToString(squad.ID) + ":" + squad.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "squad:" + util.UUIDToString(squad.ID), WorkspaceID: util.UUIDToString(squad.WorkspaceID),
		ActorType: actorType, ActorID: actorID, Payload: payload,
	}
}

func buildSquadMemberEvent(eventType string, squadID, workspaceID, memberType, memberID, actorType, actorID, action string) events.Event {
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "squad:member:" + action + ":" + squadID + ":" + memberType + ":" + memberID,
		StreamKey:      "squad:" + squadID, WorkspaceID: workspaceID, ActorType: actorType, ActorID: actorID,
		Payload: map[string]any{"squad_id": squadID, "member_type": memberType, "member_id": memberID, "action": action},
	}
}

// buildAgentDomainEvent keeps the durable agent envelope free of secret-bearing
// fields while preserving the same response shape used by the realtime API.
// The aggregate update timestamp is the version component: a retry of the same
// committed mutation reuses its idempotency key, while a later mutation gets a
// new event even when it performs the same logical action.
func (h *Handler) buildAgentDomainEvent(eventType string, agent db.Agent, actorType, actorID string) events.Event {
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "agent:" + eventType + ":" + util.UUIDToString(agent.ID) + ":" + agent.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "agent:" + util.UUIDToString(agent.ID),
		WorkspaceID:    util.UUIDToString(agent.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"agent":    broadcastAgentResponse(h.agentToResponse(agent)),
			"agent_id": util.UUIDToString(agent.ID),
		},
	}
}

func buildAgentSkillsDomainEvent(agent db.Agent, actorType, actorID, operation string, skillIDs []pgtype.UUID) events.Event {
	ids := make([]string, 0, len(skillIDs))
	for _, id := range skillIDs {
		ids = append(ids, util.UUIDToString(id))
	}
	sort.Strings(ids)
	key := strings.Join(ids, ",")
	return events.Event{
		Type:           protocol.EventAgentStatus,
		IdempotencyKey: "agent:skills:" + util.UUIDToString(agent.ID) + ":" + operation + ":" + key,
		StreamKey:      "agent:" + util.UUIDToString(agent.ID),
		WorkspaceID:    util.UUIDToString(agent.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"agent_id":  util.UUIDToString(agent.ID),
			"operation": operation,
			"skill_ids": ids,
		},
	}
}

// buildWorkspaceDomainEvent records a workspace aggregate mutation without
// coupling the outbox payload to a request or handler response.
func buildWorkspaceDomainEvent(eventType string, workspace db.Workspace, actorType, actorID string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["workspace_id"] = util.UUIDToString(workspace.ID)
	if eventType != protocol.EventWorkspaceDeleted {
		payload["workspace"] = workspaceToResponseSnapshot(workspace)
	}
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "workspace:" + eventType + ":" + util.UUIDToString(workspace.ID) + ":" + workspace.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "workspace:" + util.UUIDToString(workspace.ID),
		WorkspaceID:    util.UUIDToString(workspace.ID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

// workspaceToResponseSnapshot is deliberately free of Handler state. Avatar
// URLs are already canonical in the workspace row when this builder is used.
func workspaceToResponseSnapshot(workspace db.Workspace) map[string]any {
	var settings any
	if len(workspace.Settings) > 0 {
		_ = json.Unmarshal(workspace.Settings, &settings)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	var repos any
	if len(workspace.Repos) > 0 {
		_ = json.Unmarshal(workspace.Repos, &repos)
	}
	if repos == nil {
		repos = []any{}
	}
	return map[string]any{
		"id": uuidToString(workspace.ID), "name": workspace.Name, "slug": workspace.Slug,
		"description": textToPtr(workspace.Description), "context": textToPtr(workspace.Context),
		"settings": settings, "repos": repos, "issue_prefix": workspace.IssuePrefix,
		"avatar_url": textToPtr(workspace.AvatarUrl), "created_at": timestampToString(workspace.CreatedAt),
		"updated_at": timestampToString(workspace.UpdatedAt),
	}
}

func buildMemberDomainEvent(eventType string, member db.Member, actorType, actorID string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["member_id"] = util.UUIDToString(member.ID)
	payload["workspace_id"] = util.UUIDToString(member.WorkspaceID)
	payload["user_id"] = util.UUIDToString(member.UserID)
	if eventType != protocol.EventMemberRemoved {
		payload["member"] = memberToResponse(member)
	}
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "member:" + eventType + ":" + util.UUIDToString(member.ID) + ":" + member.Role + ":" + member.CreatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "workspace:" + util.UUIDToString(member.WorkspaceID),
		WorkspaceID:    util.UUIDToString(member.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

func buildInvitationDomainEvent(eventType string, invitation db.WorkspaceInvitation, actorType, actorID, action string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["invitation_id"] = util.UUIDToString(invitation.ID)
	payload["workspace_id"] = util.UUIDToString(invitation.WorkspaceID)
	payload["invitee_email"] = invitation.InviteeEmail
	payload["invitee_user_id"] = uuidToPtr(invitation.InviteeUserID)
	if eventType == protocol.EventInvitationCreated {
		payload["invitation"] = invitationToResponse(invitation)
	}
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "invitation:" + eventType + ":" + util.UUIDToString(invitation.ID) + ":" + action,
		StreamKey:      "workspace:" + util.UUIDToString(invitation.WorkspaceID),
		WorkspaceID:    util.UUIDToString(invitation.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

// buildAutopilotDomainEvent carries the same response-shaped payload used by
// the realtime API while giving every automation mutation a durable aggregate
// envelope. keySuffix is the changed row/version (or a child-row id), so two
// distinct mutations cannot collapse into one outbox record.
func buildAutopilotDomainEvent(eventType string, autopilot db.Autopilot, actorType, actorID, action, keySuffix string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["autopilot_id"] = util.UUIDToString(autopilot.ID)
	if action != "" {
		payload["action"] = action
	}
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "autopilot:" + eventType + ":" + util.UUIDToString(autopilot.ID) + ":" + action + ":" + keySuffix,
		StreamKey:      "autopilot:" + util.UUIDToString(autopilot.ID),
		WorkspaceID:    util.UUIDToString(autopilot.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}
