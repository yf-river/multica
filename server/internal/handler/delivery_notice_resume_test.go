package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type publishedResumeFixture struct{ delivery, token, issue, agent, original, resumed, window, candidate string }

func newPublishedResumeFixture(t *testing.T, mode ...string) publishedResumeFixture {
	t.Helper()
	if testHandler == nil {
		t.Fatal("需要真实隔离数据库")
	}
	t.Setenv("MULTICA_ENV_ID", "prod")
	f := publishedResumeFixture{}
	f.delivery, f.token = batchFixture(t)
	dbfx.QueryRow(t, `SELECT issue_id::text FROM issue_delivery WHERE id=$1`, f.delivery).Scan(&f.issue)
	daemon := dbid.NewV7().String()
	runtime := dbfx.Runtime(t, "published resume", testutil.Cols{"daemon_id": daemon})
	f.agent = dbfx.Agent(t, "published resume", runtime)
	dbfx.Exec(t, `UPDATE issue SET assignee_type='agent',assignee_id=$2,status='in_progress' WHERE id=$1`, f.issue, f.agent)
	trigger := dbfx.Comment(t, f.issue, "组合候选已验收")
	commit := strings.Repeat("c", 40)
	f.candidate = commit + "-123"
	dbfx.Exec(t, `UPDATE issue_delivery SET phase='publishing',candidate_id=$2,release_comment_id=$3,plan_snapshot=jsonb_set(plan_snapshot,'{candidate_commit}',to_jsonb($4::text)) WHERE id=$1`, f.delivery, f.candidate, trigger, commit)
	if len(mode) > 0 && mode[0] == "batch_ready" {
		dbfx.Exec(t, `UPDATE issue_delivery SET release_comment_id=$2 WHERE id=$1`, f.delivery, dbid.NewV7())
		dbfx.Exec(t, `INSERT INTO delivery_notice(delivery_id,stage,comment_id,delivered_at) VALUES($1,'batch_ready',$2,now())`, f.delivery, trigger)
	}
	if len(mode) > 0 && mode[0] == "unrelated_trigger" {
		dbfx.Exec(t, `UPDATE issue_delivery SET release_comment_id=$2 WHERE id=$1`, f.delivery, dbid.NewV7())
	}
	f.original = dbfx.Task(t, f.agent, testutil.Cols{"runtime_id": runtime, "issue_id": f.issue, "trigger_comment_id": trigger, "status": "running", "started_at": testutil.Raw("now()")})
	f.window = dbid.NewV7().String()
	if len(mode) > 0 && mode[0] == "unrelated_trigger" {
		// 用户携带的旧上下文不能冒充本次服务端绑定。
		dbfx.Exec(t, `UPDATE agent_task_queue SET context=jsonb_build_object('published_closeout',jsonb_build_object('delivery_id',$2::text,'candidate_id',$3::text,'window_id',$4::text,'release_id',$5::text)) WHERE id=$1`, f.original, f.delivery, f.candidate, f.window, commit+"-456")
	}

	dbfx.Cleanup(t, `DELETE FROM release_window WHERE id=$1`, f.window)
	dbfx.Cleanup(t, `DELETE FROM release_paused_task WHERE window_id=$1`, f.window)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE rerun_of_task_id=$1`, f.original)
	call := func(action string) {
		testutil.Call(t, testHandler.ReleaseWindow, newRequest("POST", "/window", map[string]any{"id": f.window, "action": action, "daemon_id": daemon, "release_id": commit + "-456"})).Want(200)
	}
	call("begin")
	call("pause")
	testutil.Call(t, testHandler.AckReleasePause, withURLParam(newRequest("POST", "/ack", map[string]any{"session_id": "checkpoint", "work_dir": "/private/checkpoint", "root_dir": "/private"}), "taskId", f.original)).Want(200)
	call("resume")
	call("resume")
	dbfx.QueryRow(t, `SELECT resume_task_id::text FROM release_paused_task WHERE window_id=$1`, f.window).Scan(&f.resumed)
	dbfx.Exec(t, `UPDATE issue_delivery SET phase='published',updated_at=now() WHERE id=$1`, f.delivery)
	dbfx.Exec(t, `INSERT INTO delivery_notice(delivery_id,stage,comment_id) VALUES($1,'published',$2)`, f.delivery, dbid.NewV7())
	return f
}
func (f publishedResumeFixture) notify(t *testing.T) bool {
	t.Helper()
	var body struct {
		Deferred bool `json:"notification_deferred"`
	}
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/advance", map[string]any{"id": f.delivery, "revision": 1, "worker_token": f.token, "action": "notify-result"})).Want(200).JSON(&body)
	return body.Deferred
}
func (f publishedResumeFixture) assertNotice(t *testing.T, delivered bool, count int) {
	t.Helper()
	var got bool
	var tasks int
	dbfx.QueryRow(t, `SELECT delivered_at IS NOT NULL FROM delivery_notice WHERE delivery_id=$1 AND stage='published'`, f.delivery).Scan(&got)
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue q JOIN delivery_notice n ON (n.comment_id=q.trigger_comment_id OR n.comment_id=ANY(q.coalesced_comment_ids)) WHERE n.delivery_id=$1 AND n.stage='published'`, f.delivery).Scan(&tasks)
	if got != delivered || tasks != count {
		t.Fatalf("delivered=%v tasks=%d; want %v/%d", got, tasks, delivered, count)
	}
}
func TestPublishedResumeDefersThenAuditsWithoutDuplicateExecution(t *testing.T) {
	f := newPublishedResumeFixture(t)
	for _, status := range []string{"running", "queued", "dispatched", "waiting_local_directory", "deferred"} {
		dbfx.Exec(t, `UPDATE agent_task_queue SET status=$2 WHERE id=$1`, f.resumed, status)
		for n := 0; n < 2; n++ {
			if !f.notify(t) {
				t.Fatalf("%s应等待恢复任务", status)
			}
			f.assertNotice(t, false, 0)
		}
	}
	// 进程重启只重新读取数据库，不依赖内存去重状态。
	reloaded := *testHandler
	pending, complete, err := reloaded.resumedPublishedCloseout(context.Background(), testWorkspaceID, f.issue, f.delivery, f.candidate)
	if err != nil || !pending || complete {
		t.Fatalf("重启读取: %v/%v %v", pending, complete, err)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1`, f.resumed)
	dbfx.Exec(t, `UPDATE issue SET status='done',updated_at=now() WHERE id=$1`, f.issue)
	for n := 0; n < 2; n++ {
		if f.notify(t) {
			t.Fatal("完成后应投递审计")
		}
		f.assertNotice(t, true, 0)
	}
	var comments int
	dbfx.QueryRow(t, `SELECT count(*) FROM comment c JOIN delivery_notice n ON n.comment_id=c.id WHERE n.delivery_id=$1 AND n.stage='published'`, f.delivery).Scan(&comments)
	if comments != 1 {
		t.Fatalf("审计评论=%d", comments)
	}
}
func TestPublishedResumeFallbackAndCandidateIsolation(t *testing.T) {
	for _, mode := range []string{"failed", "cancelled", "completed_without_done", "missing_pause", "wrong_candidate", "wrong_delivery", "wrong_window", "wrong_release", "other_agent", "review_task"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublishedResumeFixture(t)
			if mode != "completed_without_done" {
				dbfx.Exec(t, `UPDATE issue SET status='done',updated_at=now() WHERE id=$1`, f.issue)
			}
			dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1`, f.resumed)
			switch mode {
			case "failed", "cancelled":
				dbfx.Exec(t, `UPDATE agent_task_queue SET status=$2 WHERE id=$1`, f.resumed, mode)
			case "missing_pause":
				dbfx.Exec(t, `UPDATE release_paused_task SET resume_task_id=NULL WHERE window_id=$1`, f.window)
			case "other_agent":
				other := dbfx.Agent(t, "different owner", "")
				dbfx.Exec(t, `UPDATE issue SET assignee_id=$2 WHERE id=$1`, f.issue, other)
			case "review_task":
				dbfx.Exec(t, `UPDATE agent_task_queue SET rerun_of_task_id=NULL WHERE id=$1`, f.resumed)
			case "wrong_candidate", "wrong_delivery", "wrong_window", "wrong_release":
				key := map[string]string{"wrong_candidate": "candidate_id", "wrong_delivery": "delivery_id", "wrong_window": "window_id", "wrong_release": "release_id"}[mode]
				raw, _ := json.Marshal("different")
				dbfx.Exec(t, `UPDATE agent_task_queue SET context=jsonb_set(context,ARRAY['published_closeout',$2::text],$3::jsonb) WHERE id=$1`, f.resumed, key, raw)
			}
			for n := 0; n < 2; n++ {
				if f.notify(t) {
					t.Fatal("无有效承接不应延迟")
				}
			}
			// 改派后的无runtime负责人可能被常规路由拒绝；通知仍正常留档，不应伪称抑制。
			if mode != "other_agent" {
				f.assertNotice(t, true, 1)
			}
		})
	}
}

func TestPublishedResumeOnlyBindsActualDeliveryTrigger(t *testing.T) {
	for _, mode := range []string{"batch_ready", "unrelated_trigger"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublishedResumeFixture(t, mode)
			pending, complete, err := testHandler.resumedPublishedCloseout(context.Background(), testWorkspaceID, f.issue, f.delivery, f.candidate)
			if err != nil || complete || pending != (mode == "batch_ready") {
				t.Fatalf("绑定错误 pending=%v completed=%v err=%v", pending, complete, err)
			}
		})
	}
}

func TestPublishedResumeAuditSurvivesCompletionReconciliation(t *testing.T) {
	f := newPublishedResumeFixture(t)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed',completed_at=now(),delivered_comment_ids=ARRAY[trigger_comment_id] WHERE id=$1`, f.resumed)
	dbfx.Exec(t, `UPDATE issue SET status='done',updated_at=now() WHERE id=$1`, f.issue)
	var runtime string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent_task_queue WHERE id=$1`, f.resumed).Scan(&runtime)
	another := dbfx.Task(t, f.agent, testutil.Cols{"runtime_id": runtime, "issue_id": f.issue, "status": "completed", "completed_at": testutil.Raw("now()")})
	if f.notify(t) {
		t.Fatal("完成后应记录审计")
	}
	f.assertNotice(t, true, 0)
	for _, id := range []string{f.resumed, another} {
		task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(id))
		if err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 2; n++ {
			reloaded := *testHandler
			reloaded.reconcileCommentsOnCompletion(context.Background(), &task)
			f.assertNotice(t, true, 0)
		}
	}
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 AND id NOT IN($2,$3,$4)`, f.issue, f.original, f.resumed, another).Scan(&count)
	if count != 0 {
		t.Fatalf("审计经完成补派产生%d条额外执行", count)
	}
	var audit string
	dbfx.QueryRow(t, `SELECT c.content FROM comment c JOIN delivery_notice n ON n.comment_id=c.id WHERE n.delivery_id=$1 AND n.stage='published'`, f.delivery).Scan(&audit)
	if !isNoteComment(audit) || strings.Contains(audit, "mention://") || strings.Contains(audit, "请核对") {
		t.Fatalf("审计应为持久便签且无动作指令: %s", audit)
	}

}

func TestPublishedResumeDoneBoundary(t *testing.T) {
	t.Run("already_done_then_ordinary_edit", func(t *testing.T) {
		f := newPublishedResumeFixture(t)
		// 精确覆盖绑定时的状态检查：已done的任务不能借标题编辑冒充本次验收。
		tx, err := testHandler.TxStarter.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err = tx.Exec(context.Background(), `UPDATE issue SET status='done',updated_at=now()-interval '1 minute' WHERE id=$1`, f.issue); err != nil {
			t.Fatal(err)
		}
		if err = bindPublishedResume(context.Background(), tx, f.window, f.original, f.resumed); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
		dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1`, f.resumed)
		dbfx.Exec(t, `UPDATE issue SET title='仅编辑标题',updated_at=now() WHERE id=$1`, f.issue)
		if f.notify(t) {
			t.Fatal("已done时不应绑定或延迟")
		}
		f.assertNotice(t, true, 1)
	})
	t.Run("resumed_acceptance_finishes_before_published_record", func(t *testing.T) {
		f := newPublishedResumeFixture(t)
		dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1`, f.resumed)
		dbfx.Exec(t, `UPDATE issue SET status='done',updated_at=now() WHERE id=$1`, f.issue)
		// 批次发布完成记录可以晚于恢复执行的真实收尾，不据此启动第二次验收。
		dbfx.Exec(t, `UPDATE issue_delivery SET updated_at=now()+interval '1 second' WHERE id=$1`, f.delivery)
		if f.notify(t) {
			t.Fatal("已完成恢复应审计")
		}
		f.assertNotice(t, true, 0)
	})
}
