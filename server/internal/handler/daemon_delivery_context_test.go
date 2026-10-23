package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// claimDeliveryResponse 只解析本用例要断言的字段：领取载荷里的交付上下文。
type claimDeliveryResponse struct {
	Tasks []struct {
		ID              string `json:"id"`
		DeliveryContext *struct {
			Phase           string `json:"phase"`
			CandidateID     string `json:"candidate_id"`
			CandidateCommit string `json:"candidate_commit"`
			Environment     string `json:"environment"`
			SeedID          string `json:"seed_id"`
			SeedSHA256      string `json:"seed_sha256"`
			BatchID         string `json:"batch_id"`
			BatchStatus     string `json:"batch_status"`
			BatchMembers    int    `json:"batch_members"`
		} `json:"delivery_context"`
	} `json:"tasks"`
}

// 领取时要带上交付阶段、候选、被分配的验收环境和种子，Agent 才不用自己猜环境。
func TestClaimTasksCarriesDeliveryContext(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	runtime := createClaimReclaimRuntime(t, ctx, "Delivery context runtime")
	agent, issue := createClaimReclaimAgentAndIssue(t, ctx, runtime, "Delivery context agent")

	comment := dbfx.Insert(t, "comment", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issue, "author_type": "member", "author_id": testUserID,
		"content": "已确认的交付计划",
	})
	plan := dbfx.Insert(t, "issue_delivery_plan", testutil.Cols{
		"id": dbid.NewV7(), "workspace_id": testWorkspaceID, "issue_id": issue, "comment_id": comment,
		"comment_revision": 1, "confirmation_id": dbid.NewV7(), "confirmed_by": testUserID,
		"confirmed_at": testutil.Raw("now()"),
	})
	commit := strings.Repeat("c", 40)
	delivery := dbfx.Insert(t, "issue_delivery", testutil.Cols{
		"id": dbid.NewV7(), "workspace_id": testWorkspaceID, "issue_id": issue, "operation_id": dbid.NewV7(),
		"plan_id": plan, "plan_snapshot": testutil.Raw(fmt.Sprintf(`'{"candidate_commit":"%s"}'::jsonb`, commit)),
		"source_commit": strings.Repeat("a", 40), "source_branch": "feature/context",
		"base_commit": strings.Repeat("b", 40), "phase": "testing", "candidate_id": "candidate-7",
		"verification_comment_id": dbid.NewV7(), "release_comment_id": dbid.NewV7(),
	})
	batch := dbid.NewV7().String()
	dbfx.Insert(t, "delivery_batch", testutil.Cols{
		"id": batch, "workspace_id": testWorkspaceID, "status": "frozen", "window_seconds": 60,
		"collect_until": testutil.Raw("now()"),
	})
	dbfx.InsertNoID(t, "delivery_batch_member", testutil.Cols{
		"batch_id": batch, "delivery_id": delivery, "issue_id": issue, "source_commit": strings.Repeat("a", 40),
	}, "batch_id=$1", batch)

	// 槽位名有 CHECK 约束，只能借用 verify-1；用例结束后恢复为空闲且未启用。
	resetSlot := func() {
		testPool.Exec(ctx, `UPDATE verification_slot SET enabled=false,state='idle',workspace_id=NULL,delivery_id=NULL,worker_token=NULL,lease_expires_at=NULL WHERE name='verify-1'`)
		testPool.Exec(ctx, `DELETE FROM verification_slot_seed WHERE name='verify-1'`)
	}
	resetSlot()
	t.Cleanup(resetSlot)
	dbfx.Exec(t, `INSERT INTO verification_slot(name,enabled,state,workspace_id,delivery_id,worker_token,candidate_id)
VALUES('verify-1',true,'verifying',$1,$2,$3,'candidate-7')
ON CONFLICT(name) DO UPDATE SET enabled=true,state='verifying',workspace_id=EXCLUDED.workspace_id,delivery_id=EXCLUDED.delivery_id,worker_token=EXCLUDED.worker_token,candidate_id=EXCLUDED.candidate_id`, testWorkspaceID, delivery, dbid.NewV7())
	dbfx.Exec(t, `INSERT INTO verification_slot_seed(name,seed_id,seed_sha256,verified) VALUES('verify-1','seed-20261023-v2',$1,true)
ON CONFLICT(name) DO UPDATE SET seed_id=EXCLUDED.seed_id,seed_sha256=EXCLUDED.seed_sha256,verified=true`, strings.Repeat("d", 64))

	seedQueuedIssueTask(t, ctx, agent, runtime, issue)
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/tasks/claim",
		map[string]any{"daemon_id": batchClaimTestDaemonID, "runtime_ids": []string{runtime}, "max_tasks": 1},
		testWorkspaceID, batchClaimTestDaemonID)
	testHandler.ClaimTasksByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp claimDeliveryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Tasks) != 1 || resp.Tasks[0].DeliveryContext == nil {
		t.Fatalf("claim payload missing delivery context: %s", w.Body.String())
	}
	got := resp.Tasks[0].DeliveryContext
	if got.Phase != "testing" || got.CandidateID != "candidate-7" || got.CandidateCommit != commit {
		t.Fatalf("delivery stage/candidate wrong: %+v", got)
	}
	if got.Environment != "verify-1" || got.SeedID != "seed-20261023-v2" || got.SeedSHA256 != strings.Repeat("d", 64) {
		t.Fatalf("assigned environment or seed wrong: %+v", got)
	}
	if got.BatchID != batch || got.BatchStatus != "frozen" || got.BatchMembers != 1 {
		t.Fatalf("batch context wrong: %+v", got)
	}
}
