package main

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
)

func replayedPayload(t *testing.T, payload any) any {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal event payload: %v", err)
	}
	var replayed any
	if err := json.Unmarshal(raw, &replayed); err != nil {
		t.Fatalf("unmarshal replayed event payload: %v", err)
	}
	return replayed
}

func TestEventPayloadContractsSurviveJSONReplay(t *testing.T) {
	description := "hello"
	issuePayload := replayedPayload(t, map[string]any{
		"issue": handler.IssueResponse{
			ID:          "issue-1",
			WorkspaceID: "workspace-1",
			Description: &description,
			Status:      "in_progress",
		},
		"status_changed": true,
		"prev_status":    "todo",
	})
	issue, ok := decodeIssueEvent(events.Event{Payload: issuePayload})
	if !ok || issue.Issue.ID != "issue-1" || !issue.StatusChanged || issue.PrevStatus != "todo" {
		t.Fatalf("decoded issue payload = %#v, ok=%v", issue, ok)
	}

	commentPayload := replayedPayload(t, map[string]any{
		"comment": handler.CommentResponse{
			ID:         "comment-1",
			IssueID:    "issue-1",
			AuthorType: "member",
			AuthorID:   "member-1",
			Content:    "body",
		},
		"issue_title":  "Issue",
		"issue_status": "todo",
	})
	comment, ok := decodeCommentEvent(events.Event{Payload: commentPayload})
	if !ok || comment.Comment.ID != "comment-1" || comment.IssueTitle != "Issue" {
		t.Fatalf("decoded comment payload = %#v, ok=%v", comment, ok)
	}

	taskPayload := replayedPayload(t, map[string]any{
		"task_id":  "task-1",
		"issue_id": "issue-1",
		"agent_id": "agent-1",
	})
	task, ok := decodeTaskEvent(events.Event{Payload: taskPayload})
	if !ok || task.TaskID != "task-1" || task.AgentID != "agent-1" {
		t.Fatalf("decoded task payload = %#v, ok=%v", task, ok)
	}

	issueReactionPayload := replayedPayload(t, map[string]any{
		"reaction": handler.IssueReactionResponse{
			ID:        "issue-reaction-1",
			IssueID:   "issue-1",
			ActorType: "member",
			ActorID:   "member-1",
			Emoji:     "eyes",
		},
	})
	issueReaction, ok := decodeIssueReactionEvent(events.Event{Payload: issueReactionPayload})
	if !ok || issueReaction.Reaction.IssueID != "issue-1" || issueReaction.Reaction.Emoji != "eyes" {
		t.Fatalf("decoded issue reaction payload = %#v, ok=%v", issueReaction, ok)
	}

	commentReactionPayload := replayedPayload(t, map[string]any{
		"reaction": handler.ReactionResponse{
			ID:        "comment-reaction-1",
			CommentID: "comment-1",
			ActorType: "member",
			ActorID:   "member-1",
			Emoji:     "rocket",
		},
	})
	commentReaction, ok := decodeCommentReactionEvent(events.Event{Payload: commentReactionPayload})
	if !ok || commentReaction.Reaction.CommentID != "comment-1" || commentReaction.Reaction.Emoji != "rocket" {
		t.Fatalf("decoded comment reaction payload = %#v, ok=%v", commentReaction, ok)
	}
}
