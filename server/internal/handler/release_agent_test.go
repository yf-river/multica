package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/releaseagent"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func configureReleaseAgent(t *testing.T, project string) string {
	t.Helper()
	seedTestCatalog(t)
	req := withChatTestWorkspaceCtx(t, newRequest("PUT", "/api/release-agent/settings", map[string]any{"enabled": true, "project_id": project, "runtime_id": handlerTestRuntimeID(t)}))
	w := httptest.NewRecorder()
	testHandler.UpdateReleaseAgentSettings(w, req)
	if w.Code != 200 {
		t.Fatalf("保存发布 Agent 失败: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.AgentID == "" {
		t.Fatalf("读取发布 Agent 失败: %v %s", err, w.Body.String())
	}
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE agent_id=$1`, result.AgentID)
	return result.AgentID
}

func TestReleaseProjectVerificationEntersQueueWithoutIndividualApproval(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	dbfx.Cleanup(t, `DELETE FROM release_agent_config WHERE workspace_id=$1`, testWorkspaceID)
	dbfx.Cleanup(t, `DELETE FROM agent WHERE workspace_id=$1 AND system_key=$2`, testWorkspaceID, releaseagent.SystemKey)
	project := dbfx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "发布项目"})
	configureReleaseAgent(t, project)
	reviewer := enableReviewSettings(t, "")
	delivery, token := batchFixture(t)
	var issue string
	dbfx.QueryRow(t, `SELECT issue_id::text FROM issue_delivery WHERE id=$1`, delivery).Scan(&issue)
	dbfx.Cleanup(t, `DELETE FROM issue_review WHERE issue_id=$1`, issue)
	dbfx.Cleanup(t, `DELETE FROM verification_event WHERE delivery_id=$1`, delivery)
	dbfx.Exec(t, `UPDATE issue SET project_id=$2 WHERE id=$1`, issue, project)
	dbfx.Exec(t, `UPDATE issue_delivery SET phase='testing',approved_by=NULL,approved_at=NULL,candidate_id='verify-1',acceptance_submitted=true,evidence='[{"name":"组合业务","status":"passed","evidence":"测试通过"}]'::jsonb WHERE id=$1`, delivery)
	dbfx.Exec(t, `INSERT INTO delivery_evidence(id,workspace_id,issue_id,delivery_id,candidate_id,source_commit,identity,evidence) VALUES($1,$2,$3,$4,'verify-1',$5,'{}','[{"name":"组合业务","status":"passed","evidence":"测试通过"}]')`, dbid.NewV7(), testWorkspaceID, issue, delivery, strings.Repeat("a", 40))
	body := map[string]any{"id": delivery, "action": "accepted", "revision": 1, "worker_token": token, "source_commit": strings.Repeat("a", 40)}
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", body)).Want(409)
	body["action"] = "request-final-review"
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", body)).Want(200)
	var reviewSHA string
	dbfx.QueryRow(t, `SELECT COALESCE(context->>'head_sha','') FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2 ORDER BY created_at DESC LIMIT 1`, issue, reviewer).Scan(&reviewSHA)
	if reviewSHA != strings.Repeat("a", 40) {
		t.Fatalf("复审没有绑定测试提交: %s", reviewSHA)
	}
	if result := submitReviewVerdict(t, issue, reviewer, "pass", map[string]any{}); result.Code != 200 {
		t.Fatalf("提交复审结果失败: %d %s", result.Code, result.Body.String())
	}
	var versionBeforeTest string
	dbfx.QueryRow(t, deliveryCommentsVersionSQL, testWorkspaceID, issue).Scan(&versionBeforeTest)
	dbfx.Exec(t, `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{project_comments_version}',to_jsonb($2::text)) WHERE id=$1`, delivery, versionBeforeTest)
	dbfx.Insert(t, "comment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issue, "author_type": "member", "author_id": testUserID, "content": "测试期间的补充要求"})
	body["action"] = "accepted"
	body["revision"] = 2
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", body)).Want(200)
	var phase, status string
	var automatic, noIndividualApproval, verified bool
	dbfx.QueryRow(t, `SELECT d.phase,d.auto_release,d.approved_by IS NULL,i.status,COALESCE((i.metadata->>'verification_passed')::boolean,false) FROM issue_delivery d JOIN issue i ON i.id=d.issue_id WHERE d.id=$1`, delivery).Scan(&phase, &automatic, &noIndividualApproval, &status, &verified)
	if phase != "approved" || !automatic || !noIndividualApproval || status != "pending_release" || !verified {
		t.Fatalf("项目交付没有正确进入待发布: phase=%s automatic=%v noApproval=%v status=%s verified=%v", phase, automatic, noIndividualApproval, status, verified)
	}
	var heldVersion, latestVersion string
	dbfx.QueryRow(t, `SELECT plan_snapshot->>'project_comments_version' FROM issue_delivery WHERE id=$1`, delivery).Scan(&heldVersion)
	dbfx.QueryRow(t, deliveryCommentsVersionSQL, testWorkspaceID, issue).Scan(&latestVersion)
	if heldVersion != versionBeforeTest || heldVersion == latestVersion {
		t.Fatal("测试期间的新评论被静默标记为已核对")
	}
	// 后续测试独立验证已核对版本的状态转换。
	dbfx.Exec(t, `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{project_comments_version}',to_jsonb($2::text)) WHERE id=$1`, delivery, latestVersion)
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	ready, err := deliveryAuthorizationReady(context.Background(), tx, testWorkspaceID, issue, delivery)
	if err != nil || !ready {
		t.Fatalf("项目策略没有授权当前提交: ready=%v err=%v", ready, err)
	}
	if _, err = tx.Exec(context.Background(), `UPDATE issue SET status='in_progress' WHERE id=$1`, issue); err != nil {
		t.Fatal(err)
	}
	ready, err = deliveryAuthorizationReady(context.Background(), tx, testWorkspaceID, issue, delivery)
	if err != nil || ready {
		t.Fatalf("任务退出待发布后仍可发布: ready=%v err=%v", ready, err)
	}
	if _, err = tx.Exec(context.Background(), `UPDATE issue SET status='pending_release' WHERE id=$1`, issue); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), `UPDATE issue SET metadata=metadata||'{"release_hold":true}'::jsonb WHERE id=$1`, issue); err != nil {
		t.Fatal(err)
	}
	ready, err = deliveryAuthorizationReady(context.Background(), tx, testWorkspaceID, issue, delivery)
	if err != nil || ready {
		t.Fatalf("任务的发布暂停没有生效: ready=%v err=%v", ready, err)
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	var originalTime string
	dbfx.QueryRow(t, `SELECT approved_at::text FROM issue_delivery WHERE id=$1`, delivery).Scan(&originalTime)
	dbfx.Insert(t, "comment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issue, "author_type": "member", "author_id": testUserID, "content": "请确认现有说明"})
	var version string
	dbfx.QueryRow(t, deliveryCommentsVersionSQL, testWorkspaceID, issue).Scan(&version)
	var owner coordinationOwner
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "claim", "review-new-comment", owner, "")).Want(200).JSON(&owner)
	request := func(commentVersion, scope string) *http.Request {
		r := deliveryRequest(issue, map[string]any{"action": "review-project-comments", "revision": deliveryRevision(t, delivery), "user_comments_version": commentVersion, "scope_review": scope}, owner)
		chi.RouteContext(r.Context()).URLParams.Add("deliveryId", delivery)
		return r
	}
	testutil.Call(t, testHandler.ActIssueDelivery, request("stale", "范围未变化")).Want(409)
	testutil.Call(t, testHandler.ActIssueDelivery, request(version, "")).Want(409)
	dbfx.Exec(t, `UPDATE issue SET metadata=metadata||'{"release_hold":true}'::jsonb WHERE id=$1`, issue)
	testutil.Call(t, testHandler.ActIssueDelivery, request(version, "范围未变化")).Want(409)
	dbfx.Exec(t, `UPDATE issue SET metadata=metadata-'release_hold' WHERE id=$1`, issue)
	reviewRevision := deliveryRevision(t, delivery)
	testutil.Call(t, testHandler.ActIssueDelivery, request(version, "说明不改变交付范围，继续发布")).Want(200)
	replay := deliveryRequest(issue, map[string]any{"action": "review-project-comments", "revision": reviewRevision, "user_comments_version": version, "scope_review": "说明不改变交付范围，继续发布"}, owner)
	chi.RouteContext(replay.Context()).URLParams.Add("deliveryId", delivery)
	testutil.Call(t, testHandler.ActIssueDelivery, replay).Want(200)
	if deliveryRevision(t, delivery) != reviewRevision+1 {
		t.Fatal("重复评论复核改变了交付版本")
	}
	var savedVersion, afterTime string
	dbfx.QueryRow(t, `SELECT plan_snapshot->>'project_comments_version',approved_at::text FROM issue_delivery WHERE id=$1`, delivery).Scan(&savedVersion, &afterTime)
	if savedVersion != version || afterTime != originalTime {
		t.Fatalf("评论复核改变了排队时间或未保存版本: %s %s", savedVersion, afterTime)
	}
	tx2, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ready, err = deliveryAuthorizationReady(context.Background(), tx2, testWorkspaceID, issue, delivery)
	if err != nil || !ready {
		t.Fatalf("复核后未恢复自动发布: ready=%v err=%v", ready, err)
	}
	if err = tx2.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	dbfx.Insert(t, "comment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issue, "author_type": "member", "author_id": testUserID, "content": "新的变更"})
	tx3, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ready, err = deliveryAuthorizationReady(context.Background(), tx3, testWorkspaceID, issue, delivery)
	if err != nil || ready {
		t.Fatalf("新评论没有重新触发核对: ready=%v err=%v", ready, err)
	}
	if err = tx3.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if response := moveIssueTo(t, issue, "in_progress"); response.Code != 200 {
		t.Fatalf("任务退回进行中失败: %d %s", response.Code, response.Body.String())
	}
	var phaseAfter string
	var automaticAfter, verificationAfter bool
	dbfx.QueryRow(t, `SELECT d.phase,d.auto_release,COALESCE((i.metadata->>'verification_passed')::boolean,false) FROM issue_delivery d JOIN issue i ON i.id=d.issue_id WHERE d.id=$1`, delivery).Scan(&phaseAfter, &automaticAfter, &verificationAfter)
	if phaseAfter != "needs_changes" || automaticAfter || verificationAfter {
		t.Fatalf("任务退出待发布后未清除旧证据：phase=%s automatic=%v verified=%v", phaseAfter, automaticAfter, verificationAfter)
	}
}

func TestReleaseAgentSettingsCreatesOneManagedAgent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	dbfx.Cleanup(t, `DELETE FROM release_agent_config WHERE workspace_id=$1`, testWorkspaceID)
	dbfx.Cleanup(t, `DELETE FROM agent WHERE workspace_id=$1 AND system_key=$2`, testWorkspaceID, releaseagent.SystemKey)
	seedTestCatalog(t)
	project := dbfx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "发布项目"})
	body := map[string]any{"enabled": true, "project_id": project, "runtime_id": handlerTestRuntimeID(t)}
	update := func(body map[string]any) map[string]any {
		t.Helper()
		req := withChatTestWorkspaceCtx(t, newRequest("PUT", "/api/release-agent/settings", body))
		w := httptest.NewRecorder()
		testHandler.UpdateReleaseAgentSettings(w, req)
		if w.Code != 200 {
			t.Fatalf("保存发布 Agent 失败: %d %s", w.Code, w.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	created := update(body)
	if created["enabled"] != true || created["project_id"] != project || created["agent_id"] == "" {
		t.Fatalf("配置返回错误: %v", created)
	}
	if again := update(body); again["agent_id"] != created["agent_id"] {
		t.Fatalf("重复保存创建了多个发布 Agent: %v", again)
	}
	list := httptest.NewRecorder()
	testHandler.ListAgents(list, withChatTestWorkspaceCtx(t, newRequest("GET", "/api/agents", nil)))
	if list.Code != 200 || strings.Contains(list.Body.String(), created["agent_id"].(string)) {
		t.Fatalf("托管发布 Agent 不应出现在可指派列表: %d %s", list.Code, list.Body.String())
	}
	managed := httptest.NewRecorder()
	testHandler.ListAgents(managed, withChatTestWorkspaceCtx(t, newRequest("GET", "/api/agents?include_managed=true", nil)))
	if managed.Code != 200 || !strings.Contains(managed.Body.String(), created["agent_id"].(string)) {
		t.Fatalf("托管发布 Agent 应可解析评论作者: %d %s", managed.Code, managed.Body.String())
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent WHERE workspace_id=$1 AND system_key=$2`, testWorkspaceID, releaseagent.SystemKey).Scan(&count); err != nil || count != 1 {
		t.Fatalf("发布 Agent 数量错误: %d %v", count, err)
	}
	if paused := update(map[string]any{"enabled": false}); paused["enabled"] != false {
		t.Fatalf("没有运行时也应允许暂停: %v", paused)
	}
}

func TestReleaseAgentBatchDispatchIsDurableAndIdempotent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	dbfx.Cleanup(t, `DELETE FROM release_agent_config WHERE workspace_id=$1`, testWorkspaceID)
	dbfx.Cleanup(t, `DELETE FROM agent WHERE workspace_id=$1 AND system_key=$2`, testWorkspaceID, releaseagent.SystemKey)
	project := dbfx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "发布项目"})
	agentID := configureReleaseAgent(t, project)
	first, _ := batchFixture(t)
	second, _ := batchFixture(t)
	for _, id := range []string{first, second} {
		dbfx.Exec(t, `UPDATE issue SET project_id=$2 WHERE id=(SELECT issue_id FROM issue_delivery WHERE id=$1)`, id, project)
	}
	var state map[string]any
	batchAction(t, map[string]any{"action": "open", "window_seconds": 1, "project_id": project}).Want(200).JSON(&state)
	id := state["batch"].(map[string]any)["id"].(string)
	dbfx.Exec(t, `UPDATE delivery_batch SET collect_until=now()-interval '1 second' WHERE id=$1`, id)
	batch, _ := freezeBatch(t, id)
	body := map[string]any{"action": "dispatch-agent", "batch_id": id, "worker_token": batch.WorkerToken, "stage": "batch"}
	batchAction(t, body).Want(200)
	batchAction(t, body).Want(200)
	var count int
	var handoff string
	dbfx.QueryRow(t, `SELECT count(*),COALESCE(max(handoff_note),'') FROM agent_task_queue WHERE agent_id=$1 AND trigger_evidence_kind='issue_release'`, agentID).Scan(&count, &handoff)
	if count != 1 || !strings.Contains(handoff, id) || strings.Count(handoff, strings.Repeat("a", 40)) != 2 {
		t.Fatalf("批次派发丢失或重复: count=%d handoff=%s", count, handoff)
	}
	// 模拟派发后、通知回执前中断：按通知号找到已有运行，不再次创建任务。
	dbfx.Exec(t, `UPDATE delivery_notice SET delivered_at=NULL WHERE stage=$1`, "release_agent:batch:"+id)
	batchAction(t, body).Want(200)
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1 AND trigger_evidence_kind='issue_release'`, agentID).Scan(&count)
	if count != 1 {
		t.Fatalf("派发恢复产生重复运行: %d", count)
	}
}

func TestProjectBatchResumesAfterOwnerReviewsNewUserComment(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	dbfx.Cleanup(t, `DELETE FROM release_agent_config WHERE workspace_id=$1`, testWorkspaceID)
	dbfx.Cleanup(t, `DELETE FROM agent WHERE workspace_id=$1 AND system_key=$2`, testWorkspaceID, releaseagent.SystemKey)
	project := dbfx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "发布项目"})
	configureReleaseAgent(t, project)
	reviewer := enableReviewSettings(t, "")
	delivery, _ := batchFixture(t)
	var issue string
	dbfx.QueryRow(t, `SELECT issue_id::text FROM issue_delivery WHERE id=$1`, delivery).Scan(&issue)
	dbfx.Cleanup(t, `DELETE FROM issue_review WHERE issue_id=$1`, issue)
	dbfx.Exec(t, `UPDATE issue SET project_id=$2,status='pending_release' WHERE id=$1`, issue, project)
	dbfx.Exec(t, `UPDATE issue_delivery SET auto_release=true,approved_by=NULL,approved_at=now()-interval '20 minutes',plan_snapshot=jsonb_set(plan_snapshot,'{project_comments_version}','"old"') WHERE id=$1`, delivery)
	dbfx.Exec(t, `INSERT INTO issue_review(issue_id,workspace_id,agent_id,state,head_sha) VALUES($1,$2,$3,'passed',$4)`, issue, testWorkspaceID, reviewer, strings.Repeat("a", 40))
	dbfx.Exec(t, `INSERT INTO delivery_evidence(id,workspace_id,issue_id,delivery_id,candidate_id,source_commit,identity,evidence) VALUES($1,$2,$3,$4,'verify',$5,'{}','[{"name":"组合业务","status":"passed","evidence":"测试通过"}]')`, dbid.NewV7(), testWorkspaceID, issue, delivery, strings.Repeat("a", 40))
	var state map[string]any
	batchAction(t, map[string]any{"action": "open", "window_seconds": 1, "project_id": project}).Want(200).JSON(&state)
	id := state["batch"].(map[string]any)["id"].(string)
	batch, _ := freezeBatch(t, id)
	held, _ := batchResponse(t, batchAction(t, map[string]any{"action": "preparing", "batch_id": id, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("b", 40)}))
	if held.Status != "frozen" {
		t.Fatalf("新评论未保留批次: %s", held.Status)
	}
	var version string
	dbfx.QueryRow(t, deliveryCommentsVersionSQL, testWorkspaceID, issue).Scan(&version)
	var owner coordinationOwner
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "claim", "comment-review-batch", owner, "")).Want(200).JSON(&owner)
	request := deliveryRequest(issue, map[string]any{"action": "review-project-comments", "revision": deliveryRevision(t, delivery), "user_comments_version": version, "scope_review": "用户评论不改变交付范围"}, owner)
	chi.RouteContext(request.Context()).URLParams.Add("deliveryId", delivery)
	testutil.Call(t, testHandler.ActIssueDelivery, request).Want(200)
	resumed, members := batchResponse(t, batchAction(t, map[string]any{"action": "retry", "batch_id": id, "worker_token": batch.WorkerToken, "automatic": true}))
	if resumed.Status != "frozen" || len(members) != 1 || members[0].DeliveryID != delivery {
		t.Fatalf("评论复核后批次未续接: %+v %+v", resumed, members)
	}
	var errorText string
	dbfx.QueryRow(t, `SELECT error FROM delivery_batch WHERE id=$1`, id).Scan(&errorText)
	if errorText != "" {
		t.Fatalf("批次仍处于等待：%s", errorText)
	}
}

func TestReleaseAgentRetriesFailedIntegrationWithoutDuplicateDispatch(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	batchCleanup(t)
	dbfx.Cleanup(t, `DELETE FROM release_agent_config WHERE workspace_id=$1`, testWorkspaceID)
	dbfx.Cleanup(t, `DELETE FROM agent WHERE workspace_id=$1 AND system_key=$2`, testWorkspaceID, releaseagent.SystemKey)
	project := dbfx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "发布项目"})
	agentID := configureReleaseAgent(t, project)
	delivery, _ := batchFixture(t)
	dbfx.Exec(t, `UPDATE issue SET project_id=$2 WHERE id=(SELECT issue_id FROM issue_delivery WHERE id=$1)`, delivery, project)
	var state map[string]any
	batchAction(t, map[string]any{"action": "open", "window_seconds": 1, "project_id": project}).Want(200).JSON(&state)
	id := state["batch"].(map[string]any)["id"].(string)
	dbfx.Exec(t, `UPDATE delivery_batch SET collect_until=now()-interval '1 second' WHERE id=$1`, id)
	batch, _ := freezeBatch(t, id)
	batchAction(t, map[string]any{"action": "preparing", "batch_id": id, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("b", 40)}).Want(200)
	batchAction(t, map[string]any{"action": "validating", "batch_id": id, "worker_token": batch.WorkerToken, "candidate_id": "candidate", "candidate_commit": strings.Repeat("d", 40)}).Want(200)
	body := map[string]any{"action": "dispatch-agent", "batch_id": id, "worker_token": batch.WorkerToken, "stage": "integration"}
	batchAction(t, body).Want(200)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='failed',completed_at=now()-interval '1 minute' WHERE agent_id=$1 AND trigger_evidence_kind='issue_release'`, agentID)
	batchAction(t, body).Want(200)
	batchAction(t, body).Want(200)
	var count, queued, history int
	dbfx.QueryRow(t, `SELECT count(*),count(*) FILTER (WHERE status='queued') FROM agent_task_queue WHERE agent_id=$1 AND trigger_evidence_kind='issue_release'`, agentID).Scan(&count, &queued)
	dbfx.QueryRow(t, `SELECT count(*) FROM delivery_notice WHERE stage LIKE $1`, "release_agent:retry:integration:"+id+":%").Scan(&history)
	if count != 2 || queued != 1 || history != 1 {
		t.Fatalf("集成续派不正确：count=%d queued=%d history=%d", count, queued, history)
	}
	for n := 0; n < 3; n++ {
		dbfx.Exec(t, `UPDATE agent_task_queue SET status='failed',completed_at=now()-interval '1 minute' WHERE agent_id=$1 AND trigger_evidence_kind='issue_release' AND status='queued'`, agentID)
		batchAction(t, body).Want(200)
	}
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1 AND trigger_evidence_kind='issue_release'`, agentID).Scan(&count)
	dbfx.QueryRow(t, `SELECT count(*) FROM delivery_notice WHERE stage LIKE $1`, "release_agent:retry:integration:"+id+":%").Scan(&history)
	if count != 4 || history != 3 {
		t.Fatalf("失败续派没有遵守上限：count=%d history=%d", count, history)
	}
}

func TestReleaseAgentProjectDeletionClearsSettingsAndReusesAgent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	dbfx.Cleanup(t, `DELETE FROM release_agent_config WHERE workspace_id=$1`, testWorkspaceID)
	dbfx.Cleanup(t, `DELETE FROM agent WHERE workspace_id=$1 AND system_key=$2`, testWorkspaceID, releaseagent.SystemKey)
	first := dbfx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "旧项目"})
	agentID := configureReleaseAgent(t, first)
	req := withURLParam(newRequest("DELETE", "/api/projects/"+first, nil), "id", first)
	w := httptest.NewRecorder()
	testHandler.DeleteProject(w, req)
	if w.Code != 204 {
		t.Fatalf("删除项目失败: %d %s", w.Code, w.Body.String())
	}
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM release_agent_config WHERE workspace_id=$1`, testWorkspaceID).Scan(&count)
	if count != 0 {
		t.Fatal("删除项目后仍有发布配置")
	}
	second := dbfx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "新项目"})
	if got := configureReleaseAgent(t, second); got != agentID {
		t.Fatalf("重新配置时应复用托管 Agent: %s != %s", got, agentID)
	}
}
