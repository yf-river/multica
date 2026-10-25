package handler

import (
	"context"
	"github.com/multica-ai/multica/server/internal/testutil"
	"testing"
)

// 只有实际交付的输入才建立线程归属，排队计划不能冒充 Agent 已经收到。
func TestReplyRootOwnershipUsesDeliveredInputs(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database unavailable")
	}
	for _, delivered := range []bool{false, true} {
		name := "pending"
		if delivered {
			name = "delivered"
		}
		t.Run(name, func(t *testing.T) {
			owner := createHandlerTestAgent(t, "root owner "+name, nil)
			current := createHandlerTestAgent(t, "assignee "+name, nil)
			issueID := createCommentTriggerPreviewIssue(t, "receipt root "+name, "agent", current)
			root := dbfx.Comment(t, issueID, "original request", testutil.Cols{"author_type": "member", "author_id": testUserID})
			task := dbfx.Task(t, owner, testutil.Cols{"issue_id": issueID, "status": "completed", "completed_at": testutil.Raw("now()")})
			_, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET coalesced_comment_ids=ARRAY[$2::uuid], delivered_comment_ids=CASE WHEN $3 THEN ARRAY[$2::uuid] ELSE ARRAY[]::uuid[] END WHERE id=$1`, task, root, delivered)
			if err != nil {
				t.Fatal(err)
			}
			preview := previewCommentTriggersForTest(t, issueID, CommentTriggerPreviewRequest{Content: "continue", ParentID: &root})
			if delivered {
				requirePreviewAgents(t, preview, owner)
			} else {
				requirePreviewAgents(t, preview)
			}
		})
	}
}
