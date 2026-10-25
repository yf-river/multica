package service

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
)

// 运行结束后仍按对应触发器的实际发布者归因；审计责任不授予人类执行权限。
func TestAutopilotIssueAttributionPreservesPublisherAfterRunCloses(t *testing.T) {
	for _, status := range []string{"issue_created", "running", "completed", "failed", "skipped"} {
		t.Run(status, func(t *testing.T) {
			pool := newResolveOriginatorPool(t)
			workspaceID, owner, agentID, issueID := seedAttributionFixture(t, pool)
			publisher := seedExtraMember(t, pool, workspaceID, "run-publisher")
			fx := testutil.New(pool, workspaceID, owner)
			ap := fx.Insert(t, "autopilot", testutil.Cols{"workspace_id": workspaceID, "title": "attribution", "assignee_id": agentID, "execution_mode": "create_issue", "created_by_type": "member", "created_by_id": owner})
			trigger := fx.Insert(t, "autopilot_trigger", testutil.Cols{"autopilot_id": ap, "kind": "schedule", "cron_expression": "0 * * * *", "published_by_type": "member", "published_by_id": publisher})
			fx.Insert(t, "autopilot_run", testutil.Cols{"autopilot_id": ap, "trigger_id": trigger, "issue_id": issueID, "source": "schedule", "status": status})
			fx.Exec(t, `UPDATE issue SET origin_type='autopilot', origin_id=$1 WHERE id=$2`, ap, issueID)
			q := db.New(pool)
			issue, err := q.GetIssue(context.Background(), util.MustParseUUID(issueID))
			if err != nil {
				t.Fatal(err)
			}
			svc := &TaskService{Queries: q}
			got := svc.attributionForIssueTask(context.Background(), issue, pgtype.UUID{}, attribution.SourceCommentSource, pgtype.UUID{})
			if got.Source != attribution.SourceTriggerOwner || got.UserID.Valid || util.UUIDToString(got.AccountableUserID) != publisher {
				t.Fatalf("attribution = %+v, want publisher responsibility without originator authority", got)
			}
			_, err = q.GetLatestAutopilotRunForIssueAttribution(context.Background(), db.GetLatestAutopilotRunForIssueAttributionParams{IssueID: issue.ID, AutopilotID: issue.OriginID, WorkspaceID: util.MustParseUUID("00000000-0000-0000-0000-000000000001")})
			if err == nil {
				t.Fatal("foreign workspace returned attribution")
			}
		})
	}
}
