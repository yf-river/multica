package main

import (
	"encoding/json"

	"github.com/multica-ai/multica/server/internal/events"
)

// Event payloads cross an asynchronous persistence boundary. Consumers decode
// the wire shape instead of asserting concrete handler-layer Go types, so an
// in-memory publish and a JSON-replayed publish have identical semantics.
type issueEventPayload struct {
	Issue eventIssue `json:"issue"`

	AssigneeChanged    bool `json:"assignee_changed"`
	StatusChanged      bool `json:"status_changed"`
	PriorityChanged    bool `json:"priority_changed"`
	StartDateChanged   bool `json:"start_date_changed"`
	DueDateChanged     bool `json:"due_date_changed"`
	DescriptionChanged bool `json:"description_changed"`
	TitleChanged       bool `json:"title_changed"`

	PrevTitle        string  `json:"prev_title"`
	PrevAssigneeType *string `json:"prev_assignee_type"`
	PrevAssigneeID   *string `json:"prev_assignee_id"`
	PrevStatus       string  `json:"prev_status"`
	PrevPriority     string  `json:"prev_priority"`
	PrevStartDate    *string `json:"prev_start_date"`
	PrevDueDate      *string `json:"prev_due_date"`
	PrevDescription  *string `json:"prev_description"`
}

type eventIssue struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	Title         string  `json:"title"`
	Description   *string `json:"description"`
	Status        string  `json:"status"`
	Priority      string  `json:"priority"`
	AssigneeType  *string `json:"assignee_type"`
	AssigneeID    *string `json:"assignee_id"`
	CreatorType   string  `json:"creator_type"`
	CreatorID     string  `json:"creator_id"`
	StartDate     *string `json:"start_date"`
	DueDate       *string `json:"due_date"`
	ParentIssueID *string `json:"parent_issue_id"`
}

type commentEventPayload struct {
	Comment           eventComment `json:"comment"`
	IssueTitle        string       `json:"issue_title"`
	IssueAssigneeType *string      `json:"issue_assignee_type"`
	IssueAssigneeID   *string      `json:"issue_assignee_id"`
	IssueStatus       string       `json:"issue_status"`
}

type eventComment struct {
	ID         string `json:"id"`
	IssueID    string `json:"issue_id"`
	AuthorType string `json:"author_type"`
	AuthorID   string `json:"author_id"`
	Content    string `json:"content"`
}

type taskEventPayload struct {
	TaskID  string `json:"task_id"`
	IssueID string `json:"issue_id"`
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
}

type issueReactionEventPayload struct {
	Reaction eventIssueReaction `json:"reaction"`
}

type eventIssueReaction struct {
	ID        string `json:"id"`
	IssueID   string `json:"issue_id"`
	ActorType string `json:"actor_type"`
	ActorID   string `json:"actor_id"`
	Emoji     string `json:"emoji"`
}

type commentReactionEventPayload struct {
	Reaction eventCommentReaction `json:"reaction"`
}

type eventCommentReaction struct {
	ID        string `json:"id"`
	CommentID string `json:"comment_id"`
	ActorType string `json:"actor_type"`
	ActorID   string `json:"actor_id"`
	Emoji     string `json:"emoji"`
}

func decodeEventPayload[T any](event events.Event) (T, bool) {
	var payload T
	raw, err := json.Marshal(event.Payload)
	if err != nil {
		return payload, false
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, false
	}
	return payload, true
}

func decodeIssueEvent(event events.Event) (issueEventPayload, bool) {
	payload, ok := decodeEventPayload[issueEventPayload](event)
	return payload, ok && payload.Issue.ID != ""
}

func decodeCommentEvent(event events.Event) (commentEventPayload, bool) {
	payload, ok := decodeEventPayload[commentEventPayload](event)
	return payload, ok && payload.Comment.ID != "" && payload.Comment.IssueID != ""
}

func decodeTaskEvent(event events.Event) (taskEventPayload, bool) {
	payload, ok := decodeEventPayload[taskEventPayload](event)
	return payload, ok
}

func decodeIssueReactionEvent(event events.Event) (issueReactionEventPayload, bool) {
	payload, ok := decodeEventPayload[issueReactionEventPayload](event)
	reaction := payload.Reaction
	return payload, ok && reaction.ID != "" && reaction.IssueID != "" && reaction.ActorID != "" && reaction.Emoji != ""
}

func decodeCommentReactionEvent(event events.Event) (commentReactionEventPayload, bool) {
	payload, ok := decodeEventPayload[commentReactionEventPayload](event)
	reaction := payload.Reaction
	return payload, ok && reaction.ID != "" && reaction.CommentID != "" && reaction.ActorID != "" && reaction.Emoji != ""
}
