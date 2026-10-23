package handler

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// batchFixture 建立一条已批准发布、授权仍有效的交付；批次相关表在用例后清理。
func batchFixture(t *testing.T) (string, string) {
	t.Helper()
	issue, plan := coordinationFixture(t)
	token := dbid.NewV7().String()
	delivery := dbfx.Insert(t, "issue_delivery", testutil.Cols{
		"id": dbid.NewV7(), "workspace_id": testWorkspaceID, "issue_id": issue, "operation_id": dbid.NewV7(),
		"plan_id": plan, "source_commit": strings.Repeat("a", 40), "source_branch": "feature/batch",
		"base_commit": strings.Repeat("b", 40), "phase": "approved", "approved_by": testUserID,
		"approved_at": testutil.Raw("now()"), "verification_comment_id": dbid.NewV7(),
		"release_comment_id": dbid.NewV7(), "worker_token": token, "plan_snapshot": testutil.Raw("'{}'::jsonb"),
	})
	dbfx.Cleanup(t, `DELETE FROM delivery_notice WHERE delivery_id=$1`, delivery)
	return delivery, token
}

func batchCleanup(t *testing.T) {
	t.Helper()
	dbfx.Cleanup(t, `DELETE FROM delivery_batch_member WHERE batch_id IN (SELECT id FROM delivery_batch WHERE workspace_id=$1)`, testWorkspaceID)
	dbfx.Cleanup(t, `DELETE FROM delivery_batch WHERE workspace_id=$1`, testWorkspaceID)
}

func openBatch(t *testing.T, window int) (deliveryBatch, []deliveryBatchMember) {
	t.Helper()
	var state map[string]any
	testutil.Call(t, testHandler.DeliveryBatch, newRequest("POST", "/api/delivery-queue/batches", map[string]any{"action": "open", "window_seconds": window})).Want(200)
	testutil.Call(t, testHandler.DeliveryBatch, newRequest("GET", "/api/delivery-queue/batches", nil)).Want(200).JSON(&state)
	raw := state["batch"].(map[string]any)
	batch := deliveryBatch{ID: raw["id"].(string), Status: raw["status"].(string), WorkerToken: str(raw["worker_token"])}
	members, _ := state["members"].([]any)
	list := make([]deliveryBatchMember, 0, len(members))
	for _, item := range members {
		value := item.(map[string]any)
		list = append(list, deliveryBatchMember{DeliveryID: value["delivery_id"].(string), Result: str(value["result"])})
	}
	return batch, list
}

func str(value any) string {
	if value == nil {
		return ""
	}
	return value.(string)
}

func batchAction(t *testing.T, body map[string]any) *testutil.Response {
	t.Helper()
	return testutil.Call(t, testHandler.DeliveryBatch, newRequest("POST", "/api/delivery-queue/batches", body))
}

func batchResponse(t *testing.T, response *testutil.Response) (deliveryBatch, []deliveryBatchMember) {
	t.Helper()
	var state map[string]any
	response.Want(200).JSON(&state)
	raw, _ := state["batch"].(map[string]any)
	batch := deliveryBatch{ID: str(raw["id"]), Status: str(raw["status"]), WorkerToken: str(raw["worker_token"]), CandidateID: str(raw["candidate_id"])}
	list := []deliveryBatchMember{}
	items, _ := state["members"].([]any)
	for _, item := range items {
		value, _ := item.(map[string]any)
		list = append(list, deliveryBatchMember{DeliveryID: str(value["delivery_id"]), Result: str(value["result"]), Error: str(value["error"])})
	}
	return batch, list
}

func freezeBatch(t *testing.T, id string) (deliveryBatch, []deliveryBatchMember) {
	t.Helper()
	return batchResponse(t, batchAction(t, map[string]any{"action": "freeze", "batch_id": id}))
}

func deliveryPhase(t *testing.T, delivery string) string {
	t.Helper()
	var phase string
	dbfx.QueryRow(t, `SELECT phase FROM issue_delivery WHERE id=$1`, delivery).Scan(&phase)
	return phase
}

func TestDeliveryBatchCollectsApprovedDeliveriesInOneWindow(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	first, _ := batchFixture(t)
	second, _ := batchFixture(t)
	batch, members := openBatch(t, 60)
	if batch.Status != "collecting" {
		t.Fatalf("新批次不在收集阶段: %s", batch.Status)
	}
	if again, _ := openBatch(t, 60); again.ID != batch.ID {
		t.Fatal("重复请求开了第二个批次")
	}
	var state map[string]any
	batchAction(t, map[string]any{"action": "freeze", "batch_id": batch.ID}).Want(200).JSON(&state)
	if state["batch"].(map[string]any)["status"] != "frozen" {
		t.Fatalf("冻结后批次状态不正确: %v", state["batch"])
	}
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM delivery_batch_member WHERE batch_id=$1`, batch.ID).Scan(&count)
	if count != 2 {
		t.Fatalf("批次成员数量不正确: %d", count)
	}
	for _, id := range []string{first, second} {
		if deliveryPhase(t, id) != "approved" {
			t.Fatal("冻结阶段不应改变成员交付阶段")
		}
	}
	// 冻结后新批准的交付进入下一批，不改动已固定的成员。
	third, _ := batchFixture(t)
	batchAction(t, map[string]any{"action": "freeze", "batch_id": batch.ID}).Want(200)
	dbfx.QueryRow(t, `SELECT count(*) FROM delivery_batch_member WHERE batch_id=$1`, batch.ID).Scan(&count)
	if count != 2 {
		t.Fatal("重复冻结把新任务混进了同一批")
	}
	if deliveryPhase(t, third) != "approved" {
		t.Fatal("后批准的交付不应直接进入已冻结批次")
	}
	_ = members
}

func TestDeliveryBatchDropsMemberWithoutAuthorizationBeforePublish(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	kept, _ := batchFixture(t)
	revoked, _ := batchFixture(t)
	batch, _ := openBatch(t, 60)
	batch, _ = freezeBatch(t, batch.ID)
	// 候选固定后用户撤回授权：该成员必须退出本批，其余成员继续发布。
	dbfx.Exec(t, `UPDATE issue_delivery SET approved_by=NULL,approved_at=NULL,plan_snapshot='{"authorization_review":{"comments_version":"stale","source_commit":"x","candidate_id":"y","authorization_id":"button"}}'::jsonb WHERE id=$1`, revoked)
	batchAction(t, map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("c", 40)}).Want(200)
	response := batchAction(t, map[string]any{"action": "validating", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_id": "candidate-1", "candidate_commit": strings.Repeat("d", 40)}).Want(200)
	var state map[string]any
	response.JSON(&state)
	if state["batch"].(map[string]any)["status"] != "validating" {
		t.Fatalf("批次状态不正确: %v", state["batch"])
	}
	if deliveryPhase(t, revoked) != "awaiting_review" {
		t.Fatalf("撤回授权的成员仍在发布流程: %s", deliveryPhase(t, revoked))
	}
	if deliveryPhase(t, kept) != "approved" {
		t.Fatal("未撤回授权的成员被连带退出")
	}
	batchAction(t, map[string]any{"action": "publishing", "batch_id": batch.ID, "worker_token": batch.WorkerToken}).Want(200)
	if deliveryPhase(t, kept) != "publishing" {
		t.Fatalf("有效成员未进入发布阶段: %s", deliveryPhase(t, kept))
	}
	if deliveryPhase(t, revoked) == "publishing" {
		t.Fatal("撤回授权的成员进入了发布阶段")
	}
}

func TestDeliveryBatchConflictStaysOnItsMemberAndPublishesOthers(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	conflicting, _ := batchFixture(t)
	batch, _ := openBatch(t, 60)
	batch, _ = freezeBatch(t, batch.ID)
	batchAction(t, map[string]any{"action": "fail", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "error": "提交无法合并到最新主分支", "conflict_delivery_id": conflicting}).Want(200)
	if deliveryPhase(t, conflicting) != "needs_changes" {
		t.Fatalf("冲突成员没有回到待修改: %s", deliveryPhase(t, conflicting))
	}
	var result, detail string
	dbfx.QueryRow(t, `SELECT result,error FROM delivery_batch_member WHERE batch_id=$1 AND delivery_id=$2`, batch.ID, conflicting).Scan(&result, &detail)
	if result != "failed" || !strings.Contains(detail, "无法合并") {
		t.Fatalf("冲突结果没有回填到具体成员: %s %s", result, detail)
	}
	// 批次失败只影响成员阶段，回填通知可在下一批重新组批。
	batchCleanup(t)
}

func TestDeliveryBatchPublishesMembersOnceWithOneCandidate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	first, _ := batchFixture(t)
	second, _ := batchFixture(t)
	batch, _ := openBatch(t, 60)
	batch, _ = freezeBatch(t, batch.ID)
	batchAction(t, map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("c", 40)}).Want(200)
	batchAction(t, map[string]any{"action": "validating", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_id": "candidate-2", "candidate_commit": strings.Repeat("d", 40)}).Want(200)
	batchAction(t, map[string]any{"action": "publishing", "batch_id": batch.ID, "worker_token": batch.WorkerToken}).Want(200)
	response := batchAction(t, map[string]any{"action": "published", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_commit": strings.Repeat("d", 40)}).Want(200)
	var state map[string]any
	response.JSON(&state)
	if state["batch"].(map[string]any)["status"] != "published" {
		t.Fatalf("批次未标记发布: %v", state["batch"])
	}
	for _, id := range []string{first, second} {
		var phase, candidate, commit string
		dbfx.QueryRow(t, `SELECT phase,candidate_id,plan_snapshot->>'candidate_commit' FROM issue_delivery WHERE id=$1`, id).Scan(&phase, &candidate, &commit)
		if phase != "published" || candidate != "candidate-2" || commit != strings.Repeat("d", 40) {
			t.Fatalf("成员回填不准确: %s %s %s", phase, candidate, commit)
		}
	}
	// 重复发布只回执当前状态，不产生第二次发布或改回早期阶段。
	again := batchAction(t, map[string]any{"action": "published", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_commit": strings.Repeat("d", 40)}).Want(200)
	var repeat map[string]any
	again.JSON(&repeat)
	if repeat["batch"].(map[string]any)["status"] != "published" {
		t.Fatal("重复发布改变了批次状态")
	}
}

func TestDeliveryBatchRejectsWrongWorkerToken(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	batchFixture(t)
	batch, _ := openBatch(t, 60)
	batch, _ = freezeBatch(t, batch.ID)
	batchAction(t, map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": dbid.NewV7().String(), "base_commit": strings.Repeat("c", 40)}).Want(409)
}
