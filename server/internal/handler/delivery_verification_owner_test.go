package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type verificationOwnerFixture struct {
	issue, delivery, token, agent, trigger string
	publication                            int64
}

func newVerificationOwnerFixture(t *testing.T, phase string) verificationOwnerFixture {
	t.Helper()
	if testHandler == nil {
		t.Fatal("需要本轮独占 PostgreSQL")
	}
	issue, plan := coordinationFixture(t)
	delivery, token := coordinationDelivery(t, issue, plan, phase)
	agent := dbfx.Agent(t, "verification owner", testRuntimeID)
	dbfx.Exec(t, `UPDATE issue SET assignee_type='agent',assignee_id=$2 WHERE id=$1`, issue, agent)
	var trigger string
	dbfx.QueryRow(t, `SELECT CASE WHEN phase='preparing_release' THEN release_comment_id ELSE verification_comment_id END::text FROM issue_delivery WHERE id=$1`, delivery).Scan(&trigger)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id=$1`, issue)
	f := verificationOwnerFixture{issue: issue, delivery: delivery, token: token, agent: agent, trigger: trigger}
	if phase == "preparing_release" {
		dbfx.Cleanup(t, `DELETE FROM delivery_publication WHERE resource='int-prod'`)
		var lease coordinationOwner
		testutil.Call(t, testHandler.PublicationLease, newRequest("POST", "/publication", map[string]any{"action": "claim", "delivery_id": delivery, "worker_token": token})).Want(200).JSON(&lease)
		f.publication = lease.Generation
	}
	return f
}
func (f verificationOwnerFixture) advance(t *testing.T, action, run string) map[string]any {
	t.Helper()
	var result map[string]any
	body := map[string]any{"id": f.delivery, "revision": 1, "worker_token": f.token, "source_commit": strings.Repeat("a", 40), "action": action, "publication_generation": f.publication}
	if action == "fail" {
		body["error"] = "原生验收执行已结束"
		body["verification_run_id"] = run
	}
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/advance", body)).Want(200).JSON(&result)
	return result
}
func (f verificationOwnerFixture) native(t *testing.T) string {
	t.Helper()
	dbfx.Insert(t, "comment", testutil.Cols{"id": f.trigger, "workspace_id": testWorkspaceID, "issue_id": f.issue, "author_type": "member", "author_id": testUserID, "content": "原生验收通知"})
	return dbfx.Task(t, f.agent, testutil.Cols{"runtime_id": testRuntimeID, "issue_id": f.issue, "trigger_comment_id": f.trigger, "status": "completed", "completed_at": testutil.Raw("now()-interval '1 minute'")})
}
func (f verificationOwnerFixture) notificationCount(t *testing.T) int {
	t.Helper()
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM comment WHERE id=$1`, f.trigger).Scan(&count)
	return count
}
func TestExternalVerificationOwnerDefersNotificationUntilReleaseOrExpiry(t *testing.T) {
	for _, phase := range []string{"testing", "preparing_release"} {
		for _, finish := range []string{"release", "expire"} {
			t.Run(phase+"/"+finish, func(t *testing.T) {
				f := newVerificationOwnerFixture(t, phase)
				var owner coordinationOwner
				testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(f.issue, "claim", "external-operator", owner, "")).Want(200).JSON(&owner)
				for n := 0; n < 2; n++ {
					testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(f.issue, "heartbeat", "external-operator", owner, "")).Want(200)
					if f.advance(t, "notify", "")["verification_deferred"] != true || f.notificationCount(t) != 0 {
						t.Fatal("有效外部归属期间重复派发了原生验收")
					}
				}
				if finish == "release" {
					testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(f.issue, "release", "external-operator", owner, "")).Want(200)
				} else {
					dbfx.Exec(t, `UPDATE delivery_owner SET expires_at=now()-interval '1 second' WHERE issue_id=$1`, f.issue)
				}
				f.advance(t, "notify", "")
				f.advance(t, "notify", "")
				if f.notificationCount(t) != 1 {
					t.Fatal("外部租约结束后没有恢复幂等的原生通知")
				}
			})
		}
	}
}
func TestExternalVerificationOwnerFencesStaleNativeTerminalFailure(t *testing.T) {
	for _, phase := range []string{"testing", "preparing_release"} {
		t.Run(phase, func(t *testing.T) {
			f := newVerificationOwnerFixture(t, phase)
			run := f.native(t)
			// 发布器已读到终态后，另一执行通过正常归属接口接手；失败写入须重新核对。
			var rows []map[string]any
			testutil.Call(t, testHandler.ListDeliveryQueue, newRequest("GET", "/queue?current="+f.delivery, nil)).Want(200).JSON(&rows)
			var owner coordinationOwner
			testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(f.issue, "claim", "external-race-winner", owner, "")).Want(200).JSON(&owner)
			if f.advance(t, "fail", run)["verification_deferred"] != true || deliveryPhase(t, f.delivery) != phase {
				t.Fatal("过时的原生终态回执杀掉了有效外部候选")
			}
			var revision int
			dbfx.QueryRow(t, `SELECT revision FROM issue_delivery WHERE id=$1`, f.delivery).Scan(&revision)
			if revision != 1 {
				t.Fatal("延期写入改变了候选代次")
			}
			testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(f.issue, "release", "external-race-winner", owner, "")).Want(200)
			f.advance(t, "fail", run)
			if deliveryPhase(t, f.delivery) != "needs_changes" {
				t.Fatal("归属释放后未恢复原生失败处理")
			}
		})
	}
}
func TestExternalVerificationOwnerDoesNotMaskNativeOrTechnicalFailure(t *testing.T) {
	t.Run("native-owner", func(t *testing.T) {
		f := newVerificationOwnerFixture(t, "testing")
		run := f.native(t)
		var owner coordinationOwner
		testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(f.issue, "claim", run, owner, "")).Want(200)
		f.advance(t, "fail", run)
		if deliveryPhase(t, f.delivery) != "needs_changes" {
			t.Fatal("原生负责人终态未按原协议结束")
		}
	})
	t.Run("technical-failure", func(t *testing.T) {
		f := newVerificationOwnerFixture(t, "testing")
		var owner coordinationOwner
		testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(f.issue, "claim", "external-operator", owner, "")).Want(200)
		testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/advance", map[string]any{"id": f.delivery, "revision": 1, "worker_token": f.token, "action": "fail", "error": "构建产物校验失败"})).Want(200)
		if deliveryPhase(t, f.delivery) != "needs_changes" {
			t.Fatal("技术构建失败被外部租约吞掉")
		}
	})
}
func TestExternalVerificationOwnerRejectsWrongRunOrWorkerIdentity(t *testing.T) {
	f := newVerificationOwnerFixture(t, "testing")
	f.native(t)
	var owner coordinationOwner
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(f.issue, "claim", "external-operator", owner, "")).Want(200)
	for n, token := range []string{f.token, dbid.NewV7().String()} {
		code := 409
		if n == 1 {
			code = 403
		}
		testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/advance", map[string]any{"id": f.delivery, "revision": 1, "worker_token": token, "action": "fail", "error": "终态", "verification_run_id": dbid.NewV7()})).Want(code)
	}
	if deliveryPhase(t, f.delivery) != "testing" {
		t.Fatal("错误身份改变了候选")
	}
}
func TestDeliveryOwnerAgentExecutionUsesVerifiedTaskIdentity(t *testing.T) {
	for _, mode := range []string{"valid-cross-issue", "missing-task", "mismatched-execution", "unknown-task", "other-agent", "other-workspace"} {
		t.Run(mode, func(t *testing.T) {
			issue, _ := coordinationFixture(t)
			agent := dbfx.Agent(t, "native owner", testRuntimeID)
			otherIssue := dbfx.Issue(t, "source execution")
			run := dbfx.Task(t, agent, testutil.Cols{"runtime_id": testRuntimeID, "issue_id": otherIssue, "status": "running"})
			execution, task, caller, code := run, run, agent, 200
			switch mode {
			case "missing-task":
				task, code = "", 403
			case "mismatched-execution":
				execution, code = "made-up-native-execution", 400
			case "unknown-task":
				task, execution, code = dbid.NewV7().String(), "", 403
				execution = task
			case "other-agent":
				caller, code = dbfx.Agent(t, "other agent", testRuntimeID), 403
			case "other-workspace":
				ws := dbfx.Insert(t, "workspace", testutil.Cols{"name": "other workspace", "slug": "other-" + dbid.NewV7().String()})
				runtime := dbfx.Runtime(t, "foreign runtime", testutil.Cols{"workspace_id": ws})
				caller = dbfx.Agent(t, "foreign agent", runtime, testutil.Cols{"workspace_id": ws})
				task = dbfx.Task(t, caller, testutil.Cols{"status": "running", "runtime_id": runtime})
				execution, code = task, 403
			}
			req := ownerRequest(issue, "claim", execution, coordinationOwner{}, "")
			req.Header.Set("X-Actor-Source", "task_token")
			req.Header.Set("X-Agent-ID", caller)
			req.Header.Set("X-Task-ID", task)
			testutil.Call(t, testHandler.ManageDeliveryOwner, req).Want(code)
		})
	}
}
func TestExternalVerificationOwnerBatchReadyNoticeRemainsRetryable(t *testing.T) {
	for _, finish := range []string{"release", "expire"} {
		t.Run(finish, func(t *testing.T) { testExternalBatchReadyLeaseEnd(t, finish) })
	}
}
func testExternalBatchReadyLeaseEnd(t *testing.T, finish string) {
	t.Helper()
	batchCleanup(t)
	delivery, token := batchFixture(t)
	var issue string
	dbfx.QueryRow(t, `SELECT issue_id::text FROM issue_delivery WHERE id=$1`, delivery).Scan(&issue)
	batch, _ := openBatch(t, 60)
	batch, _ = freezeBatch(t, batch.ID)
	batchAction(t, map[string]any{"action": "preparing", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "base_commit": strings.Repeat("c", 40)}).Want(200)
	batchAction(t, map[string]any{"action": "validating", "batch_id": batch.ID, "worker_token": batch.WorkerToken, "candidate_id": "external-batch-candidate", "candidate_commit": strings.Repeat("d", 40)}).Want(200)
	var owner coordinationOwner
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "claim", "external-batch-operator", owner, "")).Want(200).JSON(&owner)
	notify := func() bool {
		var revision int64
		dbfx.QueryRow(t, `SELECT revision FROM issue_delivery WHERE id=$1`, delivery).Scan(&revision)
		var out map[string]any
		testutil.Call(t, testHandler.AdvanceDelivery, newRequest(http.MethodPost, "/advance", map[string]any{"id": delivery, "revision": revision, "worker_token": token, "action": "notify-result", "stage": "batch_ready"})).Want(200).JSON(&out)
		return out["notification_deferred"] == true
	}
	if !notify() || !notify() {
		t.Fatal("组合通知未等待有效外部验收者")
	}
	var delivered bool
	dbfx.QueryRow(t, `SELECT delivered_at IS NOT NULL FROM delivery_notice WHERE delivery_id=$1 AND stage='batch_ready'`, delivery).Scan(&delivered)
	if delivered {
		t.Fatal("延迟通知被标成已投递")
	}
	if finish == "release" {
		testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "release", "external-batch-operator", owner, "")).Want(200)
	} else {
		dbfx.Exec(t, `UPDATE delivery_owner SET expires_at=now()-interval '1 second' WHERE issue_id=$1`, issue)
	}
	if notify() || notify() {
		t.Fatal("租约释放后组合通知仍被抑制")
	}
	dbfx.QueryRow(t, `SELECT delivered_at IS NOT NULL FROM delivery_notice WHERE delivery_id=$1 AND stage='batch_ready'`, delivery).Scan(&delivered)
	if !delivered {
		t.Fatal("组合通知没有恢复投递")
	}
}
func TestVerificationQueueRecognizesBatchReadyReleaseResume(t *testing.T) {
	f := newPublishedResumeFixture(t, "batch_ready")
	dbfx.Exec(t, `UPDATE issue_delivery SET phase='approved' WHERE id=$1`, f.delivery)
	var owner coordinationOwner
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(f.issue, "claim", f.resumed, owner, "")).Want(200)
	var rows []map[string]any
	testutil.Call(t, testHandler.ListDeliveryQueue, newRequest(http.MethodGet, "/queue?current="+f.delivery, nil)).Want(200).JSON(&rows)
	for _, row := range rows {
		if row["id"] != f.delivery {
			continue
		}
		native, _ := row["verification_run"].(map[string]any)
		lease, _ := row["verification_owner"].(map[string]any)
		if native["id"] != f.resumed || lease["external"] != false {
			t.Fatalf("恢复执行被误判外部：native=%v lease=%v", native, lease)
		}
		return
	}
	t.Fatal("队列没有返回候选")
}

func TestVerificationQueueRegistrationRunIsNotAcceptance(t *testing.T) {
	f := newVerificationOwnerFixture(t, "testing")
	run := dbfx.Task(t, f.agent, testutil.Cols{"runtime_id": testRuntimeID, "issue_id": f.issue, "status": "completed", "completed_at": testutil.Raw("now()-interval '1 minute'")})
	var owner coordinationOwner
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(f.issue, "claim", run, owner, "")).Want(200)
	var rows []map[string]any
	testutil.Call(t, testHandler.ListDeliveryQueue, newRequest("GET", "/queue?current="+f.delivery, nil)).Want(200).JSON(&rows)
	for _, row := range rows {
		if row["id"] != f.delivery {
			continue
		}
		lease, _ := row["verification_owner"].(map[string]any)
		if row["verification_run"] != nil || lease["external"] != false {
			t.Fatalf("登记执行被当成验收或外部成员：%v", row)
		}
		if f.advance(t, "notify", "")["verification_deferred"] == true || deliveryPhase(t, f.delivery) != "testing" {
			t.Fatal("登记执行结束阻止了新候选的真实验收通知")
		}
		return
	}
	t.Fatal("交付未返回")
}
