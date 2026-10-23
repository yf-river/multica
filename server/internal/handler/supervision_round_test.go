package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/supervision"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestUpdateIssueOpensAndClosesSupervisionRound 验证状态写入口直接开关完成轮次：
// 重复请求不会重复建轮，重开停止旧轮次，再次完成产生新序号。
func TestUpdateIssueOpensAndClosesSupervisionRound(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	var issueID string
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id, creator_type, creator_id, title, status)
      VALUES ($1,'member',$2,'监督轮次状态入口','in_progress') RETURNING id::text`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO supervision_config(workspace_id,owner_id) VALUES($1,$2) ON CONFLICT(workspace_id) DO NOTHING`, testWorkspaceID, testUserID); err != nil {
		t.Fatalf("seed supervision config: %v", err)
	}
	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM supervision_batch_round WHERE batch_id IN(SELECT id FROM supervision_batch WHERE issue_id=$1)`,
			`DELETE FROM supervision_batch WHERE issue_id=$1`,
			`DELETE FROM supervision_material WHERE issue_id=$1`,
			`DELETE FROM supervision_round WHERE issue_id=$1`,
			`DELETE FROM supervision_subject WHERE issue_id=$1`,
			`DELETE FROM issue WHERE id=$1`,
			`DELETE FROM supervision_config WHERE workspace_id=$1`,
		} {
			arg := issueID
			if strings.Contains(sql, "workspace_id=$1") {
				arg = testWorkspaceID
			}
			if _, err := testPool.Exec(context.Background(), sql, arg); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})

	previous := testHandler.Supervision
	testHandler.Supervision = supervision.New(testPool, service.NewTaskService(db.New(testPool), testPool, nil, nil))
	t.Cleanup(func() { testHandler.Supervision = previous })

	setStatus := func(status string) {
		t.Helper()
		w := httptest.NewRecorder()
		r := newRequest("PATCH", "/api/issues/"+issueID, map[string]any{"status": status})
		r = withURLParam(r, "id", issueID)
		testHandler.UpdateIssue(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("update status %s: %d %s", status, w.Code, w.Body.String())
		}
	}

	setStatus("done")
	setStatus("done")
	var count int
	var sequence int
	if err := testPool.QueryRow(ctx, `SELECT count(*),COALESCE(max(sequence),0) FROM supervision_round WHERE issue_id=$1 AND status='open'`, issueID).Scan(&count, &sequence); err != nil {
		t.Fatal(err)
	}
	if count != 1 || sequence != 1 {
		t.Fatalf("repeat status request created rounds: count=%d sequence=%d", count, sequence)
	}

	setStatus("in_progress")
	if n := countRows(t, `SELECT count(*) FROM supervision_round WHERE issue_id=$1 AND status='open'`, issueID); n != 0 {
		t.Fatal("reopened issue kept an open round")
	}

	setStatus("done")
	if err := testPool.QueryRow(ctx, `SELECT count(*),COALESCE(max(sequence),0) FROM supervision_round WHERE issue_id=$1 AND status='open'`, issueID).Scan(&count, &sequence); err != nil {
		t.Fatal(err)
	}
	if count != 1 || sequence != 2 {
		t.Fatalf("recompletion did not open a new round: count=%d sequence=%d", count, sequence)
	}
}

func countRows(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestSupervisionReadsExposeCompletionRound 验证页面接口按完成轮次返回状态、
// 轮次范围与覆盖统计，未完成任务不会显示成已监督。
func TestSupervisionReadsExposeCompletionRound(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	member, err := testHandler.getWorkspaceMember(ctx, testUserID, testWorkspaceID)
	if err != nil {
		t.Fatalf("load member: %v", err)
	}
	if _, err = testPool.Exec(ctx, `INSERT INTO supervision_config(workspace_id,owner_id,enabled) VALUES($1,$2,true)
      ON CONFLICT(workspace_id) DO UPDATE SET enabled=true`, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	var issueID, runtimeID, agentID string
	if err = testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id, creator_type, creator_id, title, status)
      VALUES ($1,'member',$2,'完成轮次只读接口','done') RETURNING id::text`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	if err = testPool.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id, name, provider, status, daemon_id, runtime_mode)
      VALUES ($1,'轮次只读夹具','codebuddy','online','no-daemon-round-read','local') RETURNING id::text`, testWorkspaceID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	if err = testPool.QueryRow(ctx, `INSERT INTO agent (workspace_id, name, runtime_mode, runtime_id, owner_id, system_key)
      VALUES ($1,'轮次只读 Agent','local',$2,$3,'quality_supervision') RETURNING id::text`, testWorkspaceID, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	var roundID string
	if err = testPool.QueryRow(ctx, `INSERT INTO supervision_round(id,workspace_id,issue_id,sequence,material_cutoff)
      VALUES (gen_random_uuid(),$1,$2,1,clock_timestamp()) RETURNING id::text`, testWorkspaceID, issueID).Scan(&roundID); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `INSERT INTO supervision_subject(workspace_id,issue_id,desired_version,reviewed_version,last_checked_at,summary)
      VALUES($1,$2,1,0,now(),'完成轮次结论') ON CONFLICT(workspace_id,issue_id) DO UPDATE SET last_checked_at=now(),summary=EXCLUDED.summary`, testWorkspaceID, issueID); err != nil {
		t.Fatal(err)
	}
	commentID := ""
	if err = testPool.QueryRow(ctx, `INSERT INTO comment(workspace_id,issue_id,author_type,author_id,content)
      VALUES($1,$2,'member',$3,'完成前的交付说明') RETURNING id::text`, testWorkspaceID, issueID, testUserID).Scan(&commentID); err != nil {
		t.Fatal(err)
	}
	// 评论由触发器登记为材料；把轮次截止时间放在它之后，模拟「完成时已包含这条材料」。
	if _, err = testPool.Exec(ctx, `UPDATE supervision_round SET material_cutoff=now()+interval '1 second' WHERE id=$1`, roundID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM supervision_material WHERE issue_id=$1`,
			`DELETE FROM supervision_subject WHERE issue_id=$1`,
			`DELETE FROM supervision_round WHERE issue_id=$1`,
			`DELETE FROM comment WHERE issue_id=$1`,
			`DELETE FROM issue WHERE id=$1`,
			`DELETE FROM agent WHERE id=(SELECT agent_id FROM supervision_config WHERE workspace_id=$1)`,
			`DELETE FROM agent_runtime WHERE workspace_id=$1 AND name='轮次只读夹具'`,
			`DELETE FROM supervision_config WHERE workspace_id=$1`,
		} {
			arg := issueID
			if strings.Contains(sql, "workspace_id=$1") {
				arg = testWorkspaceID
			}
			if _, e := testPool.Exec(context.Background(), sql, arg); e != nil {
				t.Errorf("cleanup: %v", e)
			}
		}
	})
	if _, err = testPool.Exec(ctx, `UPDATE supervision_config SET agent_id=$2 WHERE workspace_id=$1`, testWorkspaceID, agentID); err != nil {
		t.Fatal(err)
	}

	withMember := func(path string) *http.Request {
		r := newRequest("GET", path, nil)
		r = r.WithContext(middleware.SetMemberContext(r.Context(), testWorkspaceID, member))
		return r
	}
	w := httptest.NewRecorder()
	testHandler.GetSupervision(w, withMember("/api/supervision/"))
	if w.Code != http.StatusOK {
		t.Fatalf("snapshot: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	testHandler.ListSupervisionSubjects(w, withMember("/api/supervision/subjects?filter=pending"))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"round_sequence":1`) || !strings.Contains(w.Body.String(), `"pending_materials":`) {
		t.Fatalf("list lost round scope: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	r := withMember("/api/supervision/subjects/" + issueID)
	r = withURLParam(r, "issueId", issueID)
	testHandler.GetSupervisionSubject(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"sequence":1`) || !strings.Contains(body, `"round_materials":`) || !strings.Contains(body, `"status_category":"done"`) {
		t.Fatalf("detail lost round or coverage scope: %s", body)
	}
}
