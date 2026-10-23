package handler

import (
	"context"
	"github.com/go-chi/chi/v5"
	"net/http"
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
		"release_comment_id": dbid.NewV7(), "worker_token": token, "plan_snapshot": testutil.Raw(`'{"required_checks":[{"name":"组合业务","stage":"verification"},{"name":"组合业务","stage":"integration"}]}'::jsonb`),
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

func batchAcceptRequest(t *testing.T, delivery, candidate string, revision int64) *http.Request {
	t.Helper()
	var issue string
	dbfx.QueryRow(t, `SELECT issue_id::text FROM issue_delivery WHERE id=$1`, delivery).Scan(&issue)
	var owner coordinationOwner
	_ = testPool.QueryRow(context.Background(), `SELECT token::text,generation FROM delivery_owner WHERE issue_id=$1 AND execution_id=$2`, issue, "batch-test-"+delivery).Scan(&owner.Token, &owner.Generation)
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "claim", "batch-test-"+delivery, owner, "")).Want(200).JSON(&owner)
	req := deliveryRequest(issue, map[string]any{"action": "accept", "revision": revision, "candidate_id": candidate, "evidence": []deliveryCheck{{Name: "组合业务", Status: "passed", Evidence: "受控真实数据库接口验收"}}}, owner)
	chi.RouteContext(req.Context()).URLParams.Add("deliveryId", delivery)
	return req
}

func acceptBatchFixture(t *testing.T, batch string) {
	t.Helper()
	var delivery, candidate string
	var revision int64
	dbfx.QueryRow(t, `SELECT d.id::text,b.candidate_id,d.revision FROM delivery_batch b JOIN delivery_batch_member m ON m.batch_id=b.id JOIN issue_delivery d ON d.id=m.delivery_id WHERE b.id=$1 AND m.result='pending' ORDER BY m.created_at,m.delivery_id LIMIT 1`, batch).Scan(&delivery, &candidate, &revision)
	testutil.Call(t, testHandler.ActIssueDelivery, batchAcceptRequest(t, delivery, candidate, revision)).Want(200)
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
	// 候选固定前用户撤回授权：该成员在构建前退出本批，候选只按剩余成员构建。
	dbfx.Exec(t, `UPDATE issue_delivery SET approved_by=NULL,approved_at=NULL,plan_snapshot='{"authorization_review":{"comments_version":"stale","source_commit":"x","candidate_id":"y","authorization_id":"button"}}'::jsonb WHERE id=$1`, revoked)
	prepared := batchAction(t, map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("c", 40)}).Want(200)
	var preparing map[string]any
	prepared.JSON(&preparing)
	pending := 0
	for _, item := range preparing["members"].([]any) {
		if str(item.(map[string]any)["result"]) == "pending" {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("准备阶段没有把撤权成员移出构建名单: %d", pending)
	}
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
	acceptBatchFixture(t, batch.ID)
	batchAction(t, map[string]any{"action": "publishing", "batch_id": batch.ID, "worker_token": batch.WorkerToken}).Want(200)
	if deliveryPhase(t, kept) != "publishing" {
		t.Fatalf("有效成员未进入发布阶段: %s", deliveryPhase(t, kept))
	}
	if deliveryPhase(t, revoked) == "publishing" {
		t.Fatal("撤回授权的成员进入了发布阶段")
	}
}

// 候选固定后撤权：已构建的组合仍包含撤权成员代码，整批必须停下由下一批重建，剩余成员不能继续发布。
func TestDeliveryBatchStopsWhenMemberRevokedAfterCandidateFixed(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	kept, _ := batchFixture(t)
	revoked, _ := batchFixture(t)
	batch, _ := openBatch(t, 60)
	batch, _ = freezeBatch(t, batch.ID)
	batchAction(t, map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("c", 40)}).Want(200)
	batchAction(t, map[string]any{"action": "validating", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_id": "candidate-3", "candidate_commit": strings.Repeat("d", 40)}).Want(200)
	dbfx.Exec(t, `UPDATE issue_delivery SET approved_by=NULL,approved_at=NULL,plan_snapshot='{"authorization_review":{"comments_version":"stale","source_commit":"x","candidate_id":"y","authorization_id":"button"}}'::jsonb WHERE id=$1`, revoked)
	response := batchAction(t, map[string]any{"action": "publishing", "batch_id": batch.ID, "worker_token": batch.WorkerToken}).Want(200)
	var state map[string]any
	response.JSON(&state)
	final := state["batch"].(map[string]any)
	if final["status"] != "cancelled" {
		t.Fatalf("撤权后批次没有安全停止: %v", final)
	}
	if detail, _ := final["error"].(string); !strings.Contains(detail, "撤权") {
		t.Fatalf("批次停止原因没有说明撤权代码: %s", detail)
	}
	if deliveryPhase(t, kept) != "approved" {
		t.Fatalf("剩余成员被带入了旧组合发布: %s", deliveryPhase(t, kept))
	}
	if deliveryPhase(t, revoked) == "publishing" {
		t.Fatal("撤权成员进入了发布阶段")
	}
	batchCleanup(t)
}

// 构建阶段重复请求是幂等续接：进程在 preparing 退出后，重启的发布器可以沿着同一批继续。
func TestDeliveryBatchPreparingRetryKeepsBatchUsable(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	batchFixture(t)
	batch, _ := openBatch(t, 60)
	batch, _ = freezeBatch(t, batch.ID)
	body := map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("c", 40)}
	batchAction(t, body).Want(200)
	again := batchAction(t, body).Want(200)
	var state map[string]any
	again.JSON(&state)
	if state["batch"].(map[string]any)["status"] != "preparing" {
		t.Fatalf("重复准备请求破坏了批次状态: %v", state["batch"])
	}
	batchAction(t, map[string]any{"action": "validating", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_id": "candidate-4", "candidate_commit": strings.Repeat("d", 40)}).Want(200)
	batchCleanup(t)
}

// 构建前全部成员撤权：没有可构建的合法组合，批次直接取消，不能留下空候选继续推进。
func TestDeliveryBatchCancelsWhenAllMembersRevokedBeforeBuild(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	only, _ := batchFixture(t)
	batch, _ := openBatch(t, 60)
	batch, _ = freezeBatch(t, batch.ID)
	dbfx.Exec(t, `UPDATE issue_delivery SET approved_by=NULL,approved_at=NULL,plan_snapshot='{"authorization_review":{"comments_version":"stale","source_commit":"x","candidate_id":"y","authorization_id":"button"}}'::jsonb WHERE id=$1`, only)
	response := batchAction(t, map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("c", 40)}).Want(200)
	var state map[string]any
	response.JSON(&state)
	if state["batch"].(map[string]any)["status"] != "cancelled" {
		t.Fatalf("全部成员撤权后批次没有取消: %v", state["batch"])
	}
	batchCleanup(t)
}

func TestDeliveryBatchConflictStaysOnItsMemberAndPublishesOthers(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	conflicting, _ := batchFixture(t)
	remaining, _ := batchFixture(t)
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
	if deliveryPhase(t, remaining) != "approved" {
		t.Fatal("单个合并冲突阻塞了无冲突成员")
	}
	// 无冲突成员可以进入下批，失败成员等待原分支修复。
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
	acceptBatchFixture(t, batch.ID)
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

// 批次成员不建外键：删除交付时由应用在同一事务里清成员行和没有成员的批次。
func TestDeleteDeliveryDataCleansBatchMembers(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	issue, plan := coordinationFixture(t)
	delivery := dbfx.Insert(t, "issue_delivery", testutil.Cols{
		"id": dbid.NewV7(), "workspace_id": testWorkspaceID, "issue_id": issue, "operation_id": dbid.NewV7(),
		"plan_id": plan, "source_commit": strings.Repeat("a", 40), "source_branch": "feature/batch",
		"base_commit": strings.Repeat("b", 40), "phase": "approved", "approved_by": testUserID,
		"approved_at": testutil.Raw("now()"), "verification_comment_id": dbid.NewV7(),
		"release_comment_id": dbid.NewV7(), "worker_token": dbid.NewV7(), "plan_snapshot": testutil.Raw("'{}'::jsonb"),
	})
	batch := dbid.NewV7().String()
	dbfx.Insert(t, "delivery_batch", testutil.Cols{"id": batch, "workspace_id": testWorkspaceID, "status": "frozen", "window_seconds": 60, "collect_until": testutil.Raw("now()")})
	dbfx.InsertNoID(t, "delivery_batch_member", testutil.Cols{"batch_id": batch, "delivery_id": delivery, "issue_id": issue, "source_commit": strings.Repeat("a", 40)}, "batch_id=$1", batch)
	dbfx.Cleanup(t, `DELETE FROM delivery_batch WHERE id=$1`, batch)
	dbfx.Cleanup(t, `DELETE FROM delivery_notice WHERE delivery_id=$1`, delivery)

	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	if err := deleteDeliveryData(ctx, tx, testWorkspaceID, issue); err != nil {
		tx.Rollback(ctx)
		t.Fatalf("删除交付数据失败: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	var members, batches int
	dbfx.QueryRow(t, `SELECT count(*) FROM delivery_batch_member WHERE batch_id=$1`, batch).Scan(&members)
	dbfx.QueryRow(t, `SELECT count(*) FROM delivery_batch WHERE id=$1`, batch).Scan(&batches)
	if members != 0 || batches != 0 {
		t.Fatalf("删除交付后仍残留成员或空批次: members=%d batches=%d", members, batches)
	}
}

func TestDeliveryBatchAcceptanceBindsExactCandidateAndReplaysOnce(t *testing.T) {
	batchCleanup(t)
	batchFixture(t)
	batchFixture(t)
	batch, _ := openBatch(t, 60)
	batch, _ = freezeBatch(t, batch.ID)
	batchAction(t, map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("c", 40)}).Want(200)
	body := map[string]any{"action": "validating", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_id": "batch-real-api", "candidate_commit": strings.Repeat("d", 40), "candidate_url": "http://fixture.invalid"}
	batchAction(t, body).Want(200)
	batchAction(t, body).Want(200)
	batchAction(t, map[string]any{"action": "publishing", "batch_id": batch.ID, "worker_token": batch.WorkerToken}).Want(409)
	var representative, other, token, issue string
	var revision int64
	dbfx.QueryRow(t, `SELECT d.id::text,d.revision,d.worker_token::text,d.issue_id::text FROM delivery_batch_member m JOIN issue_delivery d ON d.id=m.delivery_id WHERE m.batch_id=$1 ORDER BY m.created_at,m.delivery_id LIMIT 1`, batch.ID).Scan(&representative, &revision, &token, &issue)
	dbfx.QueryRow(t, `SELECT delivery_id::text FROM delivery_batch_member WHERE batch_id=$1 AND delivery_id<>$2`, batch.ID, representative).Scan(&other)
	var otherRev int64
	dbfx.QueryRow(t, `SELECT revision FROM issue_delivery WHERE id=$1`, other).Scan(&otherRev)
	testutil.Call(t, testHandler.ActIssueDelivery, batchAcceptRequest(t, other, "batch-real-api", otherRev)).Want(409)
	testutil.Call(t, testHandler.ActIssueDelivery, batchAcceptRequest(t, representative, "wrong-candidate", revision)).Want(409)
	testutil.Call(t, testHandler.ActIssueDelivery, batchAcceptRequest(t, representative, "batch-real-api", revision)).Want(200)
	testutil.Call(t, testHandler.ActIssueDelivery, batchAcceptRequest(t, representative, "batch-real-api", revision)).Want(200)
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM delivery_evidence WHERE delivery_id=$1 AND candidate_id='batch-real-api'`, representative).Scan(&count)
	if count != 1 {
		t.Fatalf("验收重放重复记录: %d", count)
	}
	dbfx.QueryRow(t, `SELECT revision FROM issue_delivery WHERE id=$1`, representative).Scan(&revision)
	for n := 0; n < 2; n++ {
		testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{"id": representative, "revision": revision, "worker_token": token, "action": "notify-result", "stage": "batch_ready", "source_commit": strings.Repeat("a", 40)})).Want(200)
	}
	dbfx.QueryRow(t, `SELECT count(*) FROM comment c JOIN delivery_notice n ON n.comment_id=c.id WHERE n.delivery_id=$1 AND n.stage='batch_ready'`, representative).Scan(&count)
	if count != 1 {
		t.Fatalf("就绪通知重复: %d", count)
	}
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ready, err := deliveryAuthorizationReady(context.Background(), tx, testWorkspaceID, issue, representative)
	tx.Rollback(context.Background())
	if err != nil || !ready {
		t.Fatalf("发布器自身通知使授权失效: ready=%v err=%v", ready, err)
	}
	batchAction(t, map[string]any{"action": "publishing", "batch_id": batch.ID, "worker_token": batch.WorkerToken}).Want(200)
}

func TestDeliveryBatchAbortRestoresRemainingApprovedCandidate(t *testing.T) {
	batchCleanup(t)
	first, _ := batchFixture(t)
	second, _ := batchFixture(t)
	dbfx.Exec(t, `UPDATE issue_delivery SET candidate_id='single-before-batch' WHERE id IN($1,$2)`, first, second)
	batch, _ := openBatch(t, 60)
	batch, _ = freezeBatch(t, batch.ID)
	batchAction(t, map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("c", 40)}).Want(200)
	batchAction(t, map[string]any{"action": "validating", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_id": "old-combination", "candidate_commit": strings.Repeat("d", 40)}).Want(200)
	dbfx.Exec(t, `UPDATE issue_delivery SET approved_by=NULL WHERE id=$1`, first)
	// 无有效证据也先处理撤权：整个旧组合停止，剩余成员不能继续发布它。
	batchAction(t, map[string]any{"action": "publishing", "batch_id": batch.ID, "worker_token": batch.WorkerToken}).Want(200)
	var phase, candidate string
	dbfx.QueryRow(t, `SELECT phase,candidate_id FROM issue_delivery WHERE id=$1`, second).Scan(&phase, &candidate)
	if phase != "approved" || candidate != "single-before-batch" {
		t.Fatalf("撤权后未恢复剩余成员: %s %s", phase, candidate)
	}
	next, _ := openBatch(t, 60)
	next, members := freezeBatch(t, next.ID)
	if next.ID == batch.ID || next.WorkerToken == batch.WorkerToken || len(members) != 1 || members[0].DeliveryID != second {
		t.Fatalf("新批次没有独立身份: %+v %+v", next, members)
	}
}

// 真正组合失败保留授权和证据，后续自动收集排除失败成员，显式重试才恢复。
func TestDeliveryBatchFailureWaitsForExplicitRetry(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	first, token := batchFixture(t)
	second, _ := batchFixture(t)
	batch, _ := openBatch(t, 60)
	batch, _ = freezeBatch(t, batch.ID)
	batchAction(t, map[string]any{"action": "fail", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "error": "生产浏览器失败"}).Want(200)
	for _, id := range []string{first, second} {
		var phase string
		var approved bool
		dbfx.QueryRow(t, `SELECT phase,approved_by IS NOT NULL FROM issue_delivery WHERE id=$1`, id).Scan(&phase, &approved)
		if phase != "needs_changes" || !approved {
			t.Fatalf("failed member automatically retriable: %s %v", phase, approved)
		}
	}
	// 即使再来一次open/freeze，也不能自动带入原失败组合。
	next, _ := openBatch(t, 60)
	_, members := freezeBatch(t, next.ID)
	if len(members) != 0 {
		t.Fatalf("failed batch was recollected: %v", members)
	}
	var rev int64
	dbfx.QueryRow(t, `SELECT revision FROM issue_delivery WHERE id=$1`, first).Scan(&rev)
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{"id": first, "revision": rev, "worker_token": token, "source_commit": strings.Repeat("a", 40), "action": "retry"})).Want(200)
	if deliveryPhase(t, first) != "approved" || deliveryPhase(t, second) != "needs_changes" {
		t.Fatal("explicit retry did not remain scoped to its delivery")
	}
	batchCleanup(t)
}

// 冻结批次的原批准代表可以先领取占用，再按原批次协议准备及验收；其他批准任务不能扩权。
func TestDeliveryBatchRepresentativeClaimsPublicationBeforePrepare(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	dbfx.Cleanup(t, `DELETE FROM delivery_publication WHERE resource='int-prod'`)
	first, firstToken := batchFixture(t)
	second, secondToken := batchFixture(t)
	call := func(action, id, token string, generation int64, evidence string) *testutil.Response {
		return testutil.Call(t, testHandler.PublicationLease, newRequest("POST", "/api/delivery-queue/publication", map[string]any{"action": action, "delivery_id": id, "worker_token": token, "generation": generation, "recovery_evidence": evidence}))
	}
	call("claim", first, firstToken, 0, "").Want(409)
	batch, _ := openBatch(t, 60)
	call("claim", first, firstToken, 0, "").Want(409)
	batch, members := freezeBatch(t, batch.ID)
	representative, token, other, otherToken := first, firstToken, second, secondToken
	if members[0].DeliveryID != first {
		representative, token, other, otherToken = second, secondToken, first, firstToken
	}
	call("claim", other, otherToken, 0, "").Want(409)
	call("claim", representative, dbid.NewV7().String(), 0, "").Want(409)
	dbfx.Exec(t, `UPDATE issue_delivery SET source_commit=$2 WHERE id=$1`, representative, strings.Repeat("f", 40))
	call("claim", representative, token, 0, "").Want(409)
	dbfx.Exec(t, `UPDATE issue_delivery SET source_commit=$2 WHERE id=$1`, representative, strings.Repeat("a", 40))
	dbfx.Exec(t, `UPDATE issue_delivery SET approved_by=NULL WHERE id=$1`, representative)
	call("claim", representative, token, 0, "").Want(409)
	dbfx.Exec(t, `UPDATE issue_delivery SET approved_by=$2 WHERE id=$1`, representative, testUserID)
	var lease coordinationOwner
	call("claim", representative, token, 0, "").Want(200).JSON(&lease)
	batchAction(t, map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("b", 40)}).Want(200)
	call("heartbeat", representative, token, lease.Generation, "").Want(200)
	batchAction(t, map[string]any{"action": "validating", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_id": "combined-claim-test", "candidate_commit": strings.Repeat("c", 40), "candidate_url": "http://int.example"}).Want(200)
	call("check", representative, token, lease.Generation, "").Want(200)
	dbfx.Exec(t, `UPDATE delivery_publication SET expires_at=now()-interval '1 second' WHERE resource='int-prod'`)
	call("resume", representative, token, lease.Generation, "受控用例已确认原执行退出及候选身份").Want(200).JSON(&lease)
	call("check", representative, token, lease.Generation, "").Want(200)
	acceptBatchFixture(t, batch.ID)
	batchAction(t, map[string]any{"action": "publishing", "batch_id": batch.ID, "worker_token": batch.WorkerToken}).Want(200)
	call("heartbeat", representative, token, lease.Generation, "").Want(200)
	call("release", representative, token, lease.Generation, "").Want(200)
	batchCleanup(t)
}

func TestDeliveryBatchMembersSharePublicationAuthorization(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	dbfx.Cleanup(t, `DELETE FROM delivery_publication WHERE resource='int-prod'`)
	a, at := batchFixture(t)
	b, bt := batchFixture(t)
	batch, _ := openBatch(t, 60)
	batch, members := freezeBatch(t, batch.ID)
	if members[0].DeliveryID != a {
		a, b, at, bt = b, a, bt, at
	}
	publication := func(action string, generation int64) coordinationOwner {
		var lease coordinationOwner
		testutil.Call(t, testHandler.PublicationLease, newRequest("POST", "/api/delivery-queue/publication", map[string]any{"action": action, "delivery_id": a, "worker_token": at, "generation": generation, "recovery_evidence": "受控独占数据库确认原执行停止"})).Want(200).JSON(&lease)
		return lease
	}
	authorization := func(id string, want bool, generation int64) {
		t.Helper()
		var got struct {
			Ready      bool  `json:"ready"`
			Generation int64 `json:"publication_generation"`
		}
		req := withURLParam(newRequest("GET", "/api/delivery-queue/"+id+"/authorization", nil), "deliveryId", id)
		testutil.Call(t, testHandler.CheckDeliveryAuthorization, req).Want(200).JSON(&got)
		if got.Ready != want || (want && got.Generation != generation) {
			t.Fatalf("authorization %s: %+v, want ready=%v generation=%d", id, got, want, generation)
		}
	}
	lease := publication("claim", 0)
	outside, _ := batchFixture(t)
	authorization(outside, false, 0)
	authorization(a, true, lease.Generation)
	authorization(b, true, lease.Generation)
	batchAction(t, map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("b", 40)}).Want(200)
	batchAction(t, map[string]any{"action": "validating", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_id": "shared-lease-candidate", "candidate_commit": strings.Repeat("c", 40), "candidate_url": "http://int.example"}).Want(200)
	authorization(a, true, lease.Generation)
	authorization(b, true, lease.Generation)
	for _, id := range []string{a, b} {
		dbfx.Exec(t, `UPDATE issue_delivery SET candidate_id='wrong' WHERE id=$1`, id)
		authorization(id, false, 0)
		dbfx.Exec(t, `UPDATE issue_delivery SET candidate_id='shared-lease-candidate' WHERE id=$1`, id)
		dbfx.Exec(t, `UPDATE issue_delivery SET source_commit=$2 WHERE id=$1`, id, strings.Repeat("f", 40))
		authorization(id, false, 0)
		dbfx.Exec(t, `UPDATE issue_delivery SET source_commit=$2 WHERE id=$1`, id, strings.Repeat("a", 40))
		dbfx.Exec(t, `UPDATE issue_delivery SET approved_by=NULL WHERE id=$1`, id)
		authorization(id, false, 0)
		authorization(b, false, 0)
		dbfx.Exec(t, `UPDATE issue_delivery SET approved_by=$2 WHERE id=$1`, id, testUserID)
	}
	dbfx.Exec(t, `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{candidate_commit}','"wrong"') WHERE id=$1`, b)
	authorization(b, false, 0)
	dbfx.Exec(t, `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{candidate_commit}',to_jsonb($2::text)) WHERE id=$1`, b, strings.Repeat("c", 40))
	dbfx.Exec(t, `UPDATE delivery_publication SET delivery_id=$1,worker_token=$2 WHERE resource='int-prod'`, b, bt)
	authorization(a, false, 0)
	authorization(b, false, 0)
	dbfx.Exec(t, `UPDATE delivery_publication SET delivery_id=$1,worker_token=$2 WHERE resource='int-prod'`, a, at)
	dbfx.Exec(t, `UPDATE delivery_publication SET worker_token=$1 WHERE resource='int-prod'`, dbid.NewV7().String())
	authorization(a, false, 0)
	authorization(b, false, 0)
	dbfx.Exec(t, `UPDATE delivery_publication SET worker_token=$1 WHERE resource='int-prod'`, at)
	dbfx.Exec(t, `UPDATE delivery_batch_member SET result='withdrawn' WHERE batch_id=$1 AND delivery_id=$2`, batch.ID, b)
	authorization(b, false, 0)
	dbfx.Exec(t, `UPDATE delivery_batch_member SET result='pending' WHERE batch_id=$1 AND delivery_id=$2`, batch.ID, b)
	dbfx.Exec(t, `UPDATE delivery_publication SET expires_at=now()-interval '1 second' WHERE resource='int-prod'`)
	authorization(a, false, 0)
	authorization(b, false, 0)
	lease = publication("resume", lease.Generation)
	authorization(a, true, lease.Generation)
	authorization(b, true, lease.Generation)
	acceptBatchFixture(t, batch.ID)
	batchAction(t, map[string]any{"action": "publishing", "batch_id": batch.ID, "worker_token": batch.WorkerToken}).Want(200)
	authorization(a, true, lease.Generation)
	authorization(b, true, lease.Generation)
	publication("heartbeat", lease.Generation)
	publication("check", lease.Generation)
	for range 2 {
		batchAction(t, map[string]any{"action": "published", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_commit": strings.Repeat("c", 40)}).Want(200)
	}
	publication("release", lease.Generation)
}

func TestDeliveryBatchRepeatedNoticesPreserveUserCommentsVersion(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	delivery, token := batchFixture(t)
	dbfx.Exec(t, `UPDATE issue_delivery SET candidate_id='single-before-notices' WHERE id=$1`, delivery)
	var issue, version string
	dbfx.QueryRow(t, `SELECT issue_id::text FROM issue_delivery WHERE id=$1`, delivery).Scan(&issue)
	dbfx.QueryRow(t, deliveryCommentsVersionSQL, testWorkspaceID, issue).Scan(&version)
	dbfx.Exec(t, `UPDATE issue_delivery SET plan_snapshot=jsonb_build_object('authorization_review',jsonb_build_object('authorization_id','button','comments_version',$2::text,'source_commit',source_commit,'candidate_id',candidate_id)) WHERE id=$1`, delivery, version)
	var previousNotice string
	for round := 0; round < 2; round++ {
		batch, _ := openBatch(t, 60)
		batch, _ = freezeBatch(t, batch.ID)
		batchAction(t, map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("b", 40)}).Want(200)
		batchAction(t, map[string]any{"action": "validating", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_id": "repeat-notice-" + string(rune('a'+round)), "candidate_commit": strings.Repeat("c", 40)}).Want(200)
		for range 2 {
			testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{"id": delivery, "revision": 0, "worker_token": token, "action": "notify-result", "stage": "batch_ready"})).Want(200)
		}
		var currentVersion, notice string
		dbfx.QueryRow(t, deliveryCommentsVersionSQL, testWorkspaceID, issue).Scan(&currentVersion)
		if currentVersion != version {
			t.Fatalf("round %d platform notice changed user version: %s != %s", round, currentVersion, version)
		}
		var deliveryContext map[string]any
		testutil.Call(t, testHandler.GetIssueDeliveries, withURLParam(newRequest("GET", "/api/issues/"+issue+"/delivery", nil), "id", issue)).Want(200).JSON(&deliveryContext)
		if deliveryContext["user_comments_version"] != version {
			t.Fatalf("delivery context version: %v", deliveryContext["user_comments_version"])
		}
		tx, err := testPool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ready, err := deliveryAuthorizationReady(context.Background(), tx, testWorkspaceID, issue, delivery)
		tx.Rollback(context.Background())
		if err != nil || !ready {
			t.Fatalf("round %d authorization ready=%v err=%v", round, ready, err)
		}
		var snapshot string
		dbfx.QueryRow(t, `SELECT plan_snapshot::text FROM issue_delivery WHERE id=$1`, delivery).Scan(&snapshot)
		dbfx.Exec(t, `UPDATE issue_delivery SET plan_snapshot=plan_snapshot-'authorization_review' WHERE id=$1`, delivery)
		tx, err = testPool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ready, err = deliveryAuthorizationReady(context.Background(), tx, testWorkspaceID, issue, delivery)
		tx.Rollback(context.Background())
		if err != nil || !ready {
			t.Fatalf("round %d legacy approval invalidated by platform notices: %v %v", round, ready, err)
		}
		dbfx.Exec(t, `UPDATE issue_delivery SET plan_snapshot=$2::jsonb WHERE id=$1`, delivery, snapshot)
		dbfx.QueryRow(t, `SELECT comment_id::text FROM delivery_notice WHERE delivery_id=$1 AND stage='batch_ready'`, delivery).Scan(&notice)
		if round == 1 {
			if notice == previousNotice {
				t.Fatal("new batch reused previous notice")
			}
			var archived bool
			dbfx.QueryRow(t, `SELECT EXISTS(SELECT 1 FROM delivery_notice WHERE delivery_id=$1 AND stage='batch_ready_history:'||$2::text AND comment_id=$2::uuid AND delivered_at IS NOT NULL)`, delivery, previousNotice).Scan(&archived)
			if !archived {
				t.Fatal("original exact notice provenance lost")
			}
		}
		previousNotice = notice
		batchAction(t, map[string]any{"action": "abort", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "error": "受控正常换批验证"}).Want(200)
	}
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND author_type='member'`, issue).Scan(&count)
	// fixture 中的方案评论也是真实用户评论。
	if count < 2 {
		t.Fatalf("expected two actual platform comments, got %d", count)
	}
	testutil.Call(t, testHandler.CreateComment, withURLParam(newRequest("POST", "/api/issues/"+issue+"/comments", map[string]any{"content": "交付 已进入组合候选：这是真实用户新要求，不能按文案排除"}), "id", issue)).Want(201)
	var changed string
	dbfx.QueryRow(t, deliveryCommentsVersionSQL, testWorkspaceID, issue).Scan(&changed)
	if changed == version {
		t.Fatal("actual user comment was excluded")
	}
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ready, err := deliveryAuthorizationReady(context.Background(), tx, testWorkspaceID, issue, delivery)
	tx.Rollback(context.Background())
	if err != nil || ready {
		t.Fatalf("actual user comment failed to invalidate review: ready=%v err=%v", ready, err)
	}
}

func TestDeliveryBatchArchivedNoticeDoesNotQueueOrDispatch(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	delivery, token := batchFixture(t)
	id := dbid.NewV7().String()
	stage := "batch_ready_history:" + id
	dbfx.Exec(t, `UPDATE issue_delivery SET phase='cancelled' WHERE id=$1`, delivery)
	dbfx.Exec(t, `INSERT INTO delivery_notice(delivery_id,stage,comment_id) VALUES($1,$2,$3)`, delivery, stage, id)
	var queue []map[string]any
	testutil.Call(t, testHandler.ListDeliveryQueue, newRequest("GET", "/api/delivery-queue", nil)).Want(200).JSON(&queue)
	for _, row := range queue {
		if row["id"] == delivery {
			t.Fatal("archived undelivered notice kept queue alive")
		}
	}
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{"id": delivery, "revision": 0, "worker_token": token, "action": "notify-result", "stage": stage})).Want(409)
}
