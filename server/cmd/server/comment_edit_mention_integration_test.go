package main

import (
	"fmt"
	"testing"
)

func TestEditCommentTriggers(t *testing.T) {
	agentID := getAgentID(t)
	issueID := createIssue(t, "Edit comment triggers integration test")
	t.Cleanup(func() {
		clearTasks(t, issueID)
		resp := authRequest(t, "DELETE", "/api/issues/"+issueID, nil)
		_ = resp.Body.Close()
	})

	t.Run("edit adds agent mention enqueues task", func(t *testing.T) {
		clearTasks(t, issueID)
		commentID := postComment(t, issueID, "plain comment no mentions", nil)
		clearTasks(t, issueID)

		newContent := fmt.Sprintf("[@Agent](mention://agent/%s) please review", agentID)
		updateComment(t, commentID, map[string]any{
			"content":        newContent,
			"attachment_ids": []string{},
		})

		assertPendingTasks(t, issueID, 1)
	})

	t.Run("edit removes agent mention cancels task", func(t *testing.T) {
		clearTasks(t, issueID)
		content := fmt.Sprintf("[@Agent](mention://agent/%s) fix this", agentID)
		commentID := postComment(t, issueID, content, nil)

		assertPendingTasks(t, issueID, 1)

		updateComment(t, commentID, map[string]any{
			"content":        "removed the mention, nevermind",
			"attachment_ids": []string{},
		})

		assertPendingTasks(t, issueID, 0)
	})

	t.Run("edit changes content but keeps same mention re-triggers", func(t *testing.T) {
		clearTasks(t, issueID)
		content := fmt.Sprintf("[@Agent](mention://agent/%s) fix bug A", agentID)
		commentID := postComment(t, issueID, content, nil)

		assertPendingTasks(t, issueID, 1)

		clearTasks(t, issueID)

		newContent := fmt.Sprintf("[@Agent](mention://agent/%s) actually fix bug B instead", agentID)
		updateComment(t, commentID, map[string]any{
			"content":        newContent,
			"attachment_ids": []string{},
		})

		assertPendingTasks(t, issueID, 1)
	})

	t.Run("edit on agent-assigned issue cancels and re-triggers assignee task", func(t *testing.T) {
		assignedIssue := createIssueAssignedToAgent(t, "Edit assignee trigger test", agentID)
		t.Cleanup(func() {
			clearTasks(t, assignedIssue)
			resp := authRequest(t, "DELETE", "/api/issues/"+assignedIssue, nil)
			_ = resp.Body.Close()
		})
		clearTasks(t, assignedIssue)

		commentID := postComment(t, assignedIssue, "fix the login page", nil)
		assertPendingTasks(t, assignedIssue, 1)

		clearTasks(t, assignedIssue)

		updateComment(t, commentID, map[string]any{
			"content":        "actually fix the signup page instead",
			"attachment_ids": []string{},
		})

		assertPendingTasks(t, assignedIssue, 1)
	})
}
