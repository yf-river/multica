package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func businessChecks() []requiredDeliveryCheck {
	return []requiredDeliveryCheck{{Name: "真实业务", Stage: "verification"}, {Name: "组合业务", Stage: "integration"}}
}

func TestIndependentTaskRequiresExplicitIntegrationReason(t *testing.T) {
	checks := []requiredDeliveryCheck{{Name: "独立业务", Stage: "verification"}}
	if _, err := normalizeRequiredDeliveryChecks(checks); err == nil {
		t.Fatal("普通交付不能省略联调检查")
	}
	if _, err := deliveryRequiredChecks(json.RawMessage(`{"required_checks":[{"name":"独立业务","stage":"verification"}]}`)); err == nil {
		t.Fatal("省略联调原因不能接受")
	}
	if _, err := deliveryRequiredChecks(json.RawMessage(`{"required_checks":[{"name":"独立业务","stage":"verification"}],"integration_skip_reason":"只影响独立文案"}`)); err != nil {
		t.Fatalf("有理由的独立业务检查应可进入组批：%v", err)
	}
}

func TestIndependentDeliverySkipRequiresManagedProject(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	dbfx.Cleanup(t, `DELETE FROM release_agent_config WHERE workspace_id=$1`, testWorkspaceID)
	dbfx.Cleanup(t, `DELETE FROM agent WHERE workspace_id=$1 AND system_key='issue_release'`, testWorkspaceID)
	project := dbfx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "发布项目"})
	issue, _ := coordinationFixture(t)
	dbfx.Exec(t, `UPDATE issue SET project_id=$2 WHERE id=$1`, issue, project)
	var owner coordinationOwner
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "claim", "independent-check", owner, "")).Want(200).JSON(&owner)
	body := submitBody(strings.Repeat("a", 40))
	body["required_checks"] = []requiredDeliveryCheck{{Name: "独立业务", Stage: "verification"}}
	body["integration_skip_reason"] = "只影响独立文案"
	testutil.Call(t, testHandler.SubmitIssueDelivery, deliveryRequest(issue, body, owner)).Want(400)
	configureReleaseAgent(t, project)
	reviewer := enableReviewSettings(t, "")
	dbfx.Exec(t, `DELETE FROM issue_delivery_plan WHERE issue_id=$1`, issue)
	testutil.Call(t, testHandler.SubmitIssueDelivery, deliveryRequest(issue, body, owner)).Want(200)
	var snapshot json.RawMessage
	dbfx.QueryRow(t, `SELECT plan_snapshot FROM issue_delivery WHERE issue_id=$1`, issue).Scan(&snapshot)
	if !strings.Contains(string(snapshot), "只影响独立文案") {
		t.Fatalf("未保存免联调依据: %s", snapshot)
	}
	if !strings.Contains(string(snapshot), `"workflow": "project"`) || strings.Contains(string(snapshot), `"confirmed_by"`) {
		t.Fatalf("发布项目应记录任务依据，不伪造用户确认: %s", snapshot)
	}
	var stored struct {
		CommentsVersion string `json:"project_comments_version"`
	}
	var actualVersion string
	if err := json.Unmarshal(snapshot, &stored); err != nil {
		t.Fatal(err)
	}
	dbfx.QueryRow(t, deliveryCommentsVersionSQL, testWorkspaceID, issue).Scan(&actualVersion)
	if stored.CommentsVersion != actualVersion || actualVersion == "" {
		t.Fatal("登记任务时没有固定用户评论版本")
	}
	var delivery string
	var valid bool
	dbfx.QueryRow(t, `SELECT id::text FROM issue_delivery WHERE issue_id=$1`, issue).Scan(&delivery)
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/advance", map[string]any{"id": delivery, "revision": deliveryRevision(t, delivery), "action": "claim"})).Want(409)
	if response := moveIssueTo(t, issue, "in_review"); response.Code != 200 {
		t.Fatalf("进入审查失败: %s", response.Body.String())
	}
	var reviewSHA string
	dbfx.QueryRow(t, `SELECT COALESCE(context->>'head_sha','') FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2 ORDER BY created_at DESC LIMIT 1`, issue, reviewer).Scan(&reviewSHA)
	if reviewSHA != strings.Repeat("a", 40) {
		t.Fatalf("首次审查未绑定提交: %s", reviewSHA)
	}
	if response := submitReviewVerdict(t, issue, reviewer, "pass", map[string]any{}); response.Code != 200 {
		t.Fatalf("审查结果提交失败: %s", response.Body.String())
	}
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/advance", map[string]any{"id": delivery, "revision": deliveryRevision(t, delivery), "action": "claim"})).Want(200)
	dbfx.QueryRow(t, deliveryPlanValidSQL, delivery).Scan(&valid)
	if !valid {
		t.Fatal("任务依据应可供系统验证")
	}
	dbfx.Exec(t, `UPDATE issue SET title='需求已变化' WHERE id=$1`, issue)
	dbfx.QueryRow(t, deliveryPlanValidSQL, delivery).Scan(&valid)
	if valid {
		t.Fatal("需求变化后旧依据不能继续发布")
	}
}

func businessFixture(t *testing.T) (string, string, string, string, coordinationOwner) {
	t.Helper()
	issue, plan := coordinationFixture(t)
	delivery, token := coordinationDelivery(t, issue, plan, "testing")
	dbfx.Exec(t, `UPDATE issue_delivery SET candidate_id='fixed-candidate' WHERE id=$1`, delivery)
	var owner coordinationOwner
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "claim", "business-checks-test", owner, "")).Want(200).JSON(&owner)
	return issue, plan, delivery, token, owner
}

func businessAction(t *testing.T, issue, delivery string, owner coordinationOwner, body map[string]any) *testutil.Response {
	t.Helper()
	if _, exists := body["revision"]; !exists {
		body["revision"] = deliveryRevision(t, delivery)
	}
	req := deliveryRequest(issue, body, owner)
	chi.RouteContext(req.Context()).URLParams.Add("deliveryId", delivery)
	return testutil.Call(t, testHandler.ActIssueDelivery, req)
}

func TestRequiredBusinessChecksBindToPlanAndCannotBeWeakened(t *testing.T) {
	issue, plan, delivery, _, owner := businessFixture(t)
	bind := map[string]any{"action": "bind-checks", "plan_id": dbid.NewV7(), "required_checks": businessChecks()}
	businessAction(t, issue, delivery, owner, bind).Want(409)
	delete(bind, "revision")
	bind["plan_id"] = plan
	businessAction(t, issue, delivery, owner, bind).Want(200)
	boundRevision := deliveryRevision(t, delivery)
	// 响应丢失后相同请求使用旧 revision 重放，不能再次改变版本。
	businessAction(t, issue, delivery, owner, bind).Want(200)
	if deliveryRevision(t, delivery) != boundRevision {
		t.Fatal("重复绑定改变了交付版本")
	}
	bind["revision"] = boundRevision
	bind["required_checks"] = []requiredDeliveryCheck{{Name: "更少检查", Stage: "verification"}, {Name: "组合业务", Stage: "integration"}}
	businessAction(t, issue, delivery, owner, bind).Want(409)
	var snapshot json.RawMessage
	dbfx.QueryRow(t, `SELECT plan_snapshot FROM issue_delivery WHERE id=$1`, delivery).Scan(&snapshot)
	checks, err := deliveryRequiredChecks(snapshot)
	if err != nil || len(checks) != 2 {
		t.Fatalf("方案业务清单未持久化: %s %v", snapshot, err)
	}
}

func TestRequiredBusinessChecksRejectLegacyMissingAndCoordinationOnly(t *testing.T) {
	issue, plan, delivery, _, owner := businessFixture(t)
	passed := []deliveryCheck{{Name: "真实业务", Status: "passed", Evidence: "专属 fixture 实测"}}
	accept := map[string]any{"action": "accept", "candidate_id": "fixed-candidate", "evidence": passed}
	businessAction(t, issue, delivery, owner, accept).Want(409)
	businessAction(t, issue, delivery, owner, map[string]any{"action": "bind-checks", "plan_id": plan, "required_checks": businessChecks()}).Want(200)
	delete(accept, "revision")
	accept["candidate_id"] = "wrong-candidate"
	businessAction(t, issue, delivery, owner, accept).Want(409)
	delete(accept, "revision")
	accept["candidate_id"] = "fixed-candidate"
	accept["evidence"] = []deliveryCheck{{Name: "delivery-api", Status: "passed", Evidence: "HTTP200"}}
	businessAction(t, issue, delivery, owner, accept).Want(409)
	delete(accept, "revision")
	accept["evidence"] = passed
	businessAction(t, issue, delivery, owner, accept).Want(200)
	businessAction(t, issue, delivery, owner, accept).Want(200)
	var submitted bool
	dbfx.QueryRow(t, `SELECT acceptance_submitted FROM issue_delivery WHERE id=$1`, delivery).Scan(&submitted)
	if !submitted {
		t.Fatal("完整业务证据没有登记")
	}
}

func TestRequiredBusinessChecksAutomaticCoordinationStaysPartial(t *testing.T) {
	issue, plan, delivery, token, owner := businessFixture(t)
	businessAction(t, issue, delivery, owner, map[string]any{"action": "bind-checks", "plan_id": plan, "required_checks": businessChecks()}).Want(200)
	advance := func(action string, checks []deliveryCheck) *testutil.Response {
		return testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{
			"id": delivery, "revision": deliveryRevision(t, delivery), "worker_token": token, "action": action, "evidence": checks}))
	}
	var receipt map[string]any
	advance("tested", []deliveryCheck{{Name: "delivery-api", Status: "passed", Evidence: "协调通道"}}).Want(200).JSON(&receipt)
	if receipt["acceptance_submitted"] != false {
		t.Fatal("协调检查被当成业务验收")
	}
	advance("accepted", nil).Want(409)
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM delivery_evidence WHERE delivery_id=$1`, delivery).Scan(&count)
	if count != 0 {
		t.Fatal("协调结果污染了可复用的业务验收历史")
	}
	advance("tested", []deliveryCheck{{Name: "真实业务", Status: "passed", Evidence: "受控业务 fixture"}}).Want(200).JSON(&receipt)
	if receipt["acceptance_submitted"] != true {
		t.Fatal("业务程序的完整证据未通过")
	}
	advance("accepted", nil).Want(200)
}

func TestRequiredBusinessChecksPreservePublishedHistory(t *testing.T) {
	issue, plan, delivery, _, owner := businessFixture(t)
	dbfx.Exec(t, `UPDATE issue_delivery SET phase='published' WHERE id=$1`, delivery)
	businessAction(t, issue, delivery, owner, map[string]any{"action": "bind-checks", "plan_id": plan, "required_checks": businessChecks()}).Want(409)
	if deliveryPhase(t, delivery) != "published" {
		t.Fatal("历史已发布交付被重新打开")
	}
}

func TestRequiredBusinessChecksApprovalCannotBypassBusinessEvidence(t *testing.T) {
	issue, delivery, token, _ := deliveryReviewFixture(t, true)
	advanceAccepted(t, delivery, token)
	var owner coordinationOwner
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "claim", "approval-gate", owner, "")).Want(200).JSON(&owner)
	dbfx.Exec(t, `UPDATE issue_delivery SET evidence='[{"name":"delivery-api","status":"passed","evidence":"HTTP200"}]'::jsonb WHERE id=$1`, delivery)
	response := businessAction(t, issue, delivery, owner, map[string]any{"action": "approve"}).Want(409)
	if !strings.Contains(response.Body.String(), "真实功能") {
		t.Fatalf("批准必须因缺业务项拒绝: %s", response.Body.String())
	}
	if deliveryPhase(t, delivery) != "awaiting_review" {
		t.Fatal("未验业务被批准")
	}
}

func TestRequiredBusinessChecksPublisherRechecksWithValidAuthorizationAndLease(t *testing.T) {
	issue, delivery, token, authorization := deliveryReviewFixture(t, true)
	var version string
	dbfx.QueryRow(t, `SELECT md5(COALESCE(string_agg(id::text||':'||revision::text,',' ORDER BY id),'')) FROM comment WHERE workspace_id=$1 AND issue_id=$2 AND author_type='member'`, testWorkspaceID, issue).Scan(&version)
	review, _ := json.Marshal(map[string]string{"authorization_id": authorization, "scope": "原任务", "comments_version": version,
		"source_commit": strings.Repeat("a", 40), "candidate_id": "candidate-1"})
	dbfx.Exec(t, `UPDATE issue_delivery SET phase='preparing_release',approved_by=$2,approved_at=now(),release_validated=true,plan_snapshot=jsonb_set(plan_snapshot,'{authorization_review}',$3::jsonb) WHERE id=$1`, delivery, testUserID, review)
	dbfx.Cleanup(t, `DELETE FROM delivery_publication WHERE delivery_id=$1`, delivery)
	var lease coordinationOwner
	testutil.Call(t, testHandler.PublicationLease, newRequest("POST", "/api/delivery-queue/publication", map[string]any{
		"action": "claim", "delivery_id": delivery, "worker_token": token})).Want(200).JSON(&lease)
	advance := func(action string) *testutil.Response {
		return testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{
			"id": delivery, "revision": deliveryRevision(t, delivery), "worker_token": token, "action": action, "publication_generation": lease.Generation}))
	}
	authorizationReady := func(want bool) {
		var result struct {
			Ready         bool   `json:"ready"`
			BusinessError string `json:"business_error"`
		}
		req := withURLParam(newRequest("GET", "/api/delivery-queue/"+delivery+"/authorization", nil), "deliveryId", delivery)
		testutil.Call(t, testHandler.CheckDeliveryAuthorization, req).Want(200).JSON(&result)
		if result.Ready != want || (!want && !strings.Contains(result.BusinessError, "集成业务")) {
			t.Fatalf("业务发布闸门结果: %+v", result)
		}
	}
	authorizationReady(false)
	for _, action := range []string{"validated", "publishing"} {
		response := advance(action).Want(409)
		if !strings.Contains(response.Body.String(), "集成业务") {
			t.Fatalf("有效授权/租约不能代替集成检查: %s", response.Body.String())
		}
	}
	dbfx.Exec(t, `UPDATE issue_delivery SET evidence='[{"name":"集成业务","status":"passed","evidence":"当前候选实测"}]'::jsonb WHERE id=$1`, delivery)
	advance("validated").Want(200)
	advance("publishing").Want(200)
	authorizationReady(true)
}

func TestRequiredBusinessChecksBatchNeedsEveryMemberIntegration(t *testing.T) {
	batchCleanup(t)
	first, _ := batchFixture(t)
	second, _ := batchFixture(t)
	dbfx.Exec(t, `UPDATE issue_delivery SET plan_snapshot='{"required_checks":[{"name":"另一项功能","stage":"verification"},{"name":"第二成员业务","stage":"integration"}]}'::jsonb WHERE id=$1`, second)
	batch, _ := openBatch(t, 60)
	batch, _ = freezeBatch(t, batch.ID)
	batchAction(t, map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("b", 40)}).Want(200)
	batchAction(t, map[string]any{"action": "validating", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_id": "combined-fixed", "candidate_commit": strings.Repeat("c", 40)}).Want(200)
	var representative string
	dbfx.QueryRow(t, `SELECT delivery_id::text FROM delivery_batch_member WHERE batch_id=$1 AND result='pending' ORDER BY created_at,delivery_id LIMIT 1`, batch.ID).Scan(&representative)
	if representative != first {
		t.Fatal("fixture representative order changed")
	}
	req := batchAcceptRequest(t, first, "combined-fixed", deliveryRevision(t, first))
	testutil.Call(t, testHandler.ActIssueDelivery, req).Want(409)
	// 构造旧版本已放行的不完整证据，新的发布入口仍须阻止晋级。
	dbfx.Insert(t, "delivery_evidence", testutil.Cols{"id": dbid.NewV7(), "workspace_id": testWorkspaceID,
		"issue_id": testutil.Raw("(SELECT issue_id FROM issue_delivery WHERE id='" + first + "')"), "delivery_id": first,
		"candidate_id": "combined-fixed", "source_commit": strings.Repeat("c", 40), "identity": testutil.Raw("'{}'::jsonb"),
		"evidence": testutil.Raw(`'[{"name":"组合业务","status":"passed","evidence":"遗漏第二成员"}]'::jsonb`)})
	dbfx.Exec(t, `UPDATE issue_delivery SET evidence='[{"name":"组合业务","status":"passed","evidence":"遗漏第二成员"}]'::jsonb WHERE id=$1`, first)
	batchAction(t, map[string]any{"action": "publishing", "batch_id": batch.ID, "worker_token": batch.WorkerToken}).Want(409)
	var issue string
	dbfx.QueryRow(t, `SELECT issue_id::text FROM issue_delivery WHERE id=$1`, first).Scan(&issue)
	var owner coordinationOwner
	dbfx.QueryRow(t, `SELECT token::text,generation FROM delivery_owner WHERE issue_id=$1`, issue).Scan(&owner.Token, &owner.Generation)
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "claim", "batch-test-"+first, owner, "")).Want(200).JSON(&owner)
	businessAction(t, issue, first, owner, map[string]any{"action": "accept", "candidate_id": "combined-fixed", "evidence": []deliveryCheck{
		{Name: "组合业务", Status: "passed", Evidence: "共同候选第一功能"}, {Name: "第二成员业务", Status: "passed", Evidence: "共同候选第二功能"}}}).Want(200)
	batchAction(t, map[string]any{"action": "publishing", "batch_id": batch.ID, "worker_token": batch.WorkerToken}).Want(200)
}

func TestRequiredBusinessChecksNewSubmissionNeedsExplicitBusinessList(t *testing.T) {
	issue, _ := coordinationFixture(t)
	var owner coordinationOwner
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "claim", "new-business-list", owner, "")).Want(200).JSON(&owner)
	body := submitBody(strings.Repeat("a", 40))
	delete(body, "required_checks")
	result := testutil.Call(t, testHandler.SubmitIssueDelivery, deliveryRequest(issue, body, owner)).Want(400)
	if !strings.Contains(result.Body.String(), "业务验收清单") {
		t.Fatalf("错误必须指向业务清单: %s", result.Body.String())
	}
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM issue_delivery WHERE issue_id=$1`, issue).Scan(&count)
	if count != 0 {
		t.Fatal("缺清单请求生成了交付")
	}
	body["required_checks"] = businessChecks()
	testutil.Call(t, testHandler.SubmitIssueDelivery, deliveryRequest(issue, body, owner)).Want(200)
	delete(body, "required_checks")
	testutil.Call(t, testHandler.SubmitIssueDelivery, deliveryRequest(issue, body, owner)).Want(200)
}

// 1. Verification evidence cannot complete int, and an int-only lease never grants release authority.
func TestIntegrationOnlyDeliveryPreservesVerificationAndRequiresInt(t *testing.T) {
	issue, plan, delivery, token, owner := businessFixture(t)
	businessAction(t, issue, delivery, owner, map[string]any{"action": "bind-checks", "plan_id": plan, "required_checks": businessChecks()}).Want(200)
	businessAction(t, issue, delivery, owner, map[string]any{"action": "accept", "candidate_id": "fixed-candidate", "evidence": []deliveryCheck{{Name: "真实业务", Status: "passed", Evidence: "verify fixture"}}}).Want(200)
	advance := func(action, worker, candidate string, generation int64) *testutil.Response {
		return testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{"id": delivery, "revision": deliveryRevision(t, delivery), "action": action, "worker_token": worker, "candidate_id": candidate, "publication_generation": generation}))
	}
	advance("accepted", token, "", 0).Want(200)
	var receipt struct {
		Token string `json:"worker_token"`
	}
	advance("claim-integration", token, "", 0).Want(200).JSON(&receipt)
	advance("claim-integration", token, "", 0).Want(409)
	dbfx.Cleanup(t, `DELETE FROM delivery_publication WHERE delivery_id=$1`, delivery)
	var lease struct {
		Generation int64 `json:"generation"`
	}
	testutil.Call(t, testHandler.PublicationLease, newRequest("POST", "/api/delivery-queue/publication", map[string]any{"action": "claim", "delivery_id": delivery, "worker_token": receipt.Token})).Want(200).JSON(&lease)
	advance("integrated", receipt.Token, "int-candidate", lease.Generation).Want(409)
	// The fixture stands in for the external publisher's newly prepared, distinct int candidate.
	dbfx.Exec(t, `UPDATE issue_delivery SET candidate_id='int-candidate',acceptance_submitted=false WHERE id=$1`, delivery)
	businessAction(t, issue, delivery, owner, map[string]any{"action": "accept", "candidate_id": "int-candidate", "evidence": []deliveryCheck{{Name: "真实业务", Status: "passed", Evidence: "verify fixture"}}}).Want(409)
	businessAction(t, issue, delivery, owner, map[string]any{"action": "accept", "candidate_id": "int-candidate", "evidence": []deliveryCheck{{Name: "组合业务", Status: "passed", Evidence: "int fixture"}}}).Want(200)
	advance("integrated", receipt.Token, "wrong", lease.Generation).Want(409)
	advance("integrated", receipt.Token, "int-candidate", lease.Generation-1).Want(409)
	advance("integrated", receipt.Token, "int-candidate", lease.Generation).Want(200)
	advance("claim-integration", receipt.Token, "", 0).Want(409)
	advance("publishing", receipt.Token, "int-candidate", lease.Generation).Want(409)
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM delivery_evidence WHERE delivery_id=$1`, delivery).Scan(&count)
	if count != 2 {
		t.Fatalf("verification and int histories: got %d", count)
	}
	var unapproved bool
	dbfx.QueryRow(t, `SELECT approved_by IS NULL AND NOT release_validated FROM issue_delivery WHERE id=$1`, delivery).Scan(&unapproved)
	if !unapproved || deliveryPhase(t, delivery) != "awaiting_review" {
		t.Fatal("integration changed production authorization")
	}
	businessAction(t, issue, delivery, owner, map[string]any{"action": "approve"}).Want(200)
}

func TestIntegratedApprovalRejectsUnrelatedVerificationHistory(t *testing.T) {
	for _, scenario := range []string{"different-source", "different-delivery", "incomplete-history", "missing-int", "unfinished-int"} {
		t.Run(scenario, func(t *testing.T) {
			issue, plan, delivery, _, owner := businessFixture(t)
			businessAction(t, issue, delivery, owner, map[string]any{"action": "bind-checks", "plan_id": plan, "required_checks": businessChecks()}).Want(200)
			dbfx.Exec(t, `UPDATE issue_delivery SET phase='awaiting_review',candidate_id='int-candidate',evidence='[{"name":"组合业务","status":"passed","evidence":"当前int"}]',plan_snapshot=plan_snapshot||'{"integration_complete":true}'::jsonb WHERE id=$1`, delivery)
			var source string
			dbfx.QueryRow(t, `SELECT source_commit FROM issue_delivery WHERE id=$1`, delivery).Scan(&source)
			historyDelivery := delivery
			history := `[{"name":"真实业务","status":"passed","evidence":"已接受的verify"}]`
			switch scenario {
			case "different-source":
				source = strings.Repeat("f", 40)
			case "different-delivery":
				historyDelivery, _ = coordinationDelivery(t, issue, plan, "needs_changes")
			case "incomplete-history":
				history = `[{"name":"无关检查","status":"passed","evidence":"不满足清单"}]`
			case "missing-int":
				dbfx.Exec(t, `UPDATE issue_delivery SET evidence='[]' WHERE id=$1`, delivery)
			case "unfinished-int":
				dbfx.Exec(t, `UPDATE issue_delivery SET plan_snapshot=plan_snapshot- 'integration_complete' WHERE id=$1`, delivery)
			}
			dbfx.Exec(t, `INSERT INTO delivery_evidence(id,workspace_id,issue_id,delivery_id,candidate_id,source_commit,identity,evidence) VALUES($1,$2,$3,$4,'verify-candidate',$5,'{}',$6::jsonb)`, dbid.NewV7(), testWorkspaceID, issue, historyDelivery, source, history)
			businessAction(t, issue, delivery, owner, map[string]any{"action": "approve"}).Want(409)
		})
	}
}
