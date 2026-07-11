package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestCreateCommentRollsBackWhenTriggeredTaskFails(t *testing.T) {
	agentID := createHandlerTestAgent(t, "atomic-comment-agent-"+uuid.NewString(), nil)
	issue := createHandlerAssignedCommentIssueFixture(t, "atomic comment task "+uuid.NewString(), agentID)
	installIssueTaskFailureTrigger(t, "INSERT", fmt.Sprintf("NEW.issue_id = '%s'::uuid", issue.ID))
	content := "comment and triggered task must commit together " + uuid.NewString()

	w, _ := issue.postComment(t, map[string]any{"content": content}, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	var commentCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM comment WHERE issue_id = $1 AND content = $2
	`, issue.ID, content).Scan(&commentCount); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if commentCount != 0 {
		t.Fatalf("comment committed without triggered task: %d rows", commentCount)
	}
	var eventCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = $1 AND stream_key = 'issue:' || $2
		  AND payload #>> '{comment,content}' = $3
	`, protocol.EventCommentCreated, issue.ID, content).Scan(&eventCount); err != nil {
		t.Fatalf("count comment events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("comment event committed without triggered task: %d rows", eventCount)
	}
}
