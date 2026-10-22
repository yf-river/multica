package handler

import (
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"testing"
)

func TestReleaseWindowPauseResumeIsDurableAndIdempotent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	t.Setenv("MULTICA_ENV_ID", "int")
	daemon := dbid.NewV7().String()
	window := dbid.NewV7().String()
	runtime := dbfx.Runtime(t, "release pause", testutil.Cols{"daemon_id": daemon})
	agent := dbfx.Agent(t, "release pause", runtime)
	issue := dbfx.Issue(t, "release pause", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "issue_id": issue, "status": "running", "started_at": testutil.Raw("now()")})
	dbfx.Cleanup(t, `DELETE FROM release_window WHERE id=$1`, window)
	dbfx.Cleanup(t, `DELETE FROM release_paused_task WHERE window_id=$1`, window)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE rerun_of_task_id=$1`, task)
	call := func(action string) *testutil.Response {
		return testutil.Call(t, testHandler.ReleaseWindow, newRequest("POST", "/window", map[string]any{"id": window, "action": action, "daemon_id": daemon, "release_id": "candidate"}))
	}
	call("begin").Want(200)
	call("begin").Want(200)
	call("pause").Want(200)
	call("pause").Want(200)
	var status map[string]string
	testutil.Call(t, testHandler.GetTaskStatus, withURLParam(newRequest("GET", "/status", nil), "taskId", task)).Want(200).JSON(&status)
	if status["status"] != "release_paused" {
		t.Fatal(status)
	}
	call("resume").Want(409)
	ack := map[string]any{"session_id": "original-session", "work_dir": "/preserved/worktree", "root_dir": "/preserved"}
	testutil.Call(t, testHandler.AckReleasePause, withURLParam(newRequest("POST", "/ack", ack), "taskId", task)).Want(200)
	call("resume").Want(200)
	call("resume").Want(200)
	var count int
	var session, workdir string
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE rerun_of_task_id=$1`, task).Scan(&count)
	if count != 1 {
		t.Fatalf("续接执行数量=%d", count)
	}
	dbfx.QueryRow(t, `SELECT session_id,work_dir FROM agent_task_queue WHERE id=$1`, task).Scan(&session, &workdir)
	if session != "original-session" || workdir != "/preserved/worktree" {
		t.Fatalf("原执行上下文丢失: %s %s", session, workdir)
	}
}

func TestReleaseWindowDoesNotRestartUserCancelledTask(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	t.Setenv("MULTICA_ENV_ID", "int")
	daemon := dbid.NewV7().String()
	window := dbid.NewV7().String()
	runtime := dbfx.Runtime(t, "cancel paused", testutil.Cols{"daemon_id": daemon})
	agent := dbfx.Agent(t, "cancel paused", runtime)
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "status": "running"})
	dbfx.Cleanup(t, `DELETE FROM release_window WHERE id=$1`, window)
	dbfx.Cleanup(t, `DELETE FROM release_paused_task WHERE window_id=$1`, window)
	call := func(action string) *testutil.Response {
		return testutil.Call(t, testHandler.ReleaseWindow, newRequest("POST", "/window", map[string]any{"id": window, "action": action, "daemon_id": daemon, "release_id": "candidate"}))
	}
	call("begin").Want(200)
	call("pause").Want(200)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='cancelled',completed_at=now() WHERE id=$1`, task)
	call("resume").Want(200)
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE rerun_of_task_id=$1`, task).Scan(&count)
	if count != 0 {
		t.Fatal("用户取消的任务被重启")
	}
}
