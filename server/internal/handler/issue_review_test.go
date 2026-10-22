package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/review"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// 审查产品能力的集成测试：配置托管 Agent、进入审核中派发、打回唤醒、通过写绿勾、
// 停滞停止自动返工。依赖 handler 包的测试数据库夹具。

func cleanupReviewFixture(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM review_config WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM issue_review WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND system_key = $2`, testWorkspaceID, review.SystemKey)
	})
}

func enableReviewSettings(t *testing.T, instructions string) string {
	t.Helper()
	cleanupReviewFixture(t)
	seedTestCatalog(t)
	req := newRequest("PUT", "/api/review/settings", map[string]any{
		"enabled":      true,
		"runtime_id":   handlerTestRuntimeID(t),
		"instructions": instructions,
	})
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.UpdateReviewSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enable review settings: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		AgentID    string `json:"agent_id"`
		Enabled    bool   `json:"enabled"`
		Configured bool   `json:"configured"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode review settings: %v", err)
	}
	if !resp.Enabled || !resp.Configured || resp.AgentID == "" {
		t.Fatalf("review settings not enabled: %s", w.Body.String())
	}
	return resp.AgentID
}

func moveIssueTo(t *testing.T, issueID, status string) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": status})
	req = withURLParam(req, "id", issueID)
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.UpdateIssue(w, req)
	return w
}

func submitReviewVerdict(t *testing.T, issueID, agentID, verdict string, findings map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest("POST", "/api/issues/"+issueID+"/review-verdict", map[string]any{
		"verdict":  verdict,
		"findings": findings,
	})
	req = withURLParam(req, "id", issueID)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", "00000000-0000-0000-0000-000000000001")
	req = withChatTestWorkspaceCtx(t, req)
	w := httptest.NewRecorder()
	testHandler.SubmitIssueReviewVerdict(w, req)
	return w
}

func reviewTaskHandoff(t *testing.T, issueID, agentID string) string {
	t.Helper()
	var handoff string
	if err := testPool.QueryRow(context.Background(), `SELECT COALESCE(handoff_note,'') FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 ORDER BY created_at DESC LIMIT 1`, issueID, agentID).Scan(&handoff); err != nil {
		t.Fatalf("load review task: %v", err)
	}
	return handoff
}

// finishReviewTasks 模拟审查运行结束：真实环境里审查 Agent 提交结论时任务仍在跑，
// 开发者改完再送回审核中时它已经结束，不该继续挡住下一轮派发。
func finishReviewTasks(t *testing.T, issueID, agentID string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed'
		WHERE issue_id=$1 AND agent_id=$2 AND status IN ('queued','dispatched')`, issueID, agentID); err != nil {
		t.Fatalf("finish review tasks: %v", err)
	}
}

func issueStatus(t *testing.T, issueID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("load issue status: %v", err)
	}
	return status
}

func TestReviewSettingsManageHiddenAgent(t *testing.T) {
	agentID := enableReviewSettings(t, "")

	var systemKey string
	if err := testPool.QueryRow(context.Background(), `SELECT system_key FROM agent WHERE id = $1`, agentID).Scan(&systemKey); err != nil {
		t.Fatalf("load managed agent: %v", err)
	}
	if systemKey != review.SystemKey {
		t.Fatalf("system_key = %q, want %q", systemKey, review.SystemKey)
	}

	// 智能体列表不出现审查员。
	listReq := withChatTestWorkspaceCtx(t, newRequest("GET", "/api/agents", nil))
	listW := httptest.NewRecorder()
	testHandler.ListAgents(listW, listReq)
	if strings.Contains(listW.Body.String(), agentID) {
		t.Fatalf("managed review agent leaked into the agent list: %s", listW.Body.String())
	}

	// 显式 include_managed 时可见：评论作者名需要解析它。
	managedReq := withChatTestWorkspaceCtx(t, newRequest("GET", "/api/agents?include_managed=true", nil))
	managedW := httptest.NewRecorder()
	testHandler.ListAgents(managedW, managedReq)
	if !strings.Contains(managedW.Body.String(), agentID) {
		t.Fatalf("include_managed did not return the review agent: %s", managedW.Body.String())
	}

	// 通用 Agent 更新入口拒绝修改托管审查员。
	updateReq := withChatTestWorkspaceCtx(t, newRequest("PUT", "/api/agents/"+agentID, map[string]any{"name": "改名"}))
	updateReq = withURLParam(updateReq, "id", agentID)
	updateW := httptest.NewRecorder()
	testHandler.UpdateAgent(updateW, updateReq)
	if updateW.Code != http.StatusForbidden {
		t.Fatalf("update managed agent = %d, want 403", updateW.Code)
	}
}

func TestReviewRoundRejectsWakesDeveloperAndPassWritesGreenCheck(t *testing.T) {
	reviewAgentID := enableReviewSettings(t, "")
	devAgentID := createHandlerTestAgent(t, "Review Dev Agent", []byte("[]"))
	issueID := dbfx.Issue(t, "review flow", testutil.Cols{
		"status":        "todo",
		"assignee_type": "agent",
		"assignee_id":   devAgentID,
	})

	if w := moveIssueTo(t, issueID, "in_review"); w.Code != http.StatusOK {
		t.Fatalf("move to review: %d %s", w.Code, w.Body.String())
	}
	handoff := reviewTaskHandoff(t, issueID, reviewAgentID)
	if !strings.Contains(handoff, "第 1 轮") {
		t.Fatalf("round 1 handoff missing: %s", handoff)
	}

	// 打回：状态回到进行中，并唤醒原开发 Agent。
	if w := submitReviewVerdict(t, issueID, reviewAgentID, "reject", map[string]any{
		"summary": "缺少错误处理",
		"items": []map[string]any{
			{"level": "blocking", "file": "server/internal/example.go", "detail": "错误被吞掉"},
		},
	}); w.Code != http.StatusOK {
		t.Fatalf("reject verdict: %d %s", w.Code, w.Body.String())
	}
	if status := issueStatus(t, issueID); status != "in_progress" {
		t.Fatalf("status after reject = %q, want in_progress", status)
	}
	finishReviewTasks(t, issueID, reviewAgentID)
	var devTasks int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2`, issueID, devAgentID).Scan(&devTasks); err != nil {
		t.Fatalf("count developer tasks: %v", err)
	}
	if devTasks == 0 {
		t.Fatal("reject did not wake the developer agent")
	}

	// 再次提交审核：进入第 2 轮，并把上一轮问题带进开场。
	if w := moveIssueTo(t, issueID, "in_review"); w.Code != http.StatusOK {
		t.Fatalf("move back to review: %d %s", w.Code, w.Body.String())
	}
	handoff = reviewTaskHandoff(t, issueID, reviewAgentID)
	if !strings.Contains(handoff, "第 2 轮") || !strings.Contains(handoff, "server/internal/example.go") {
		t.Fatalf("round 2 handoff missing previous findings: %s", handoff)
	}

	// 通过：状态留在审核中，写绿勾标记。
	if w := submitReviewVerdict(t, issueID, reviewAgentID, "pass", map[string]any{"summary": "已修好"}); w.Code != http.StatusOK {
		t.Fatalf("pass verdict: %d %s", w.Code, w.Body.String())
	}
	if status := issueStatus(t, issueID); status != "in_review" {
		t.Fatalf("status after pass = %q, want in_review", status)
	}
	var passed bool
	if err := testPool.QueryRow(context.Background(), `SELECT (metadata->>'review_passed')::boolean FROM issue WHERE id = $1`, issueID).Scan(&passed); err != nil {
		t.Fatalf("load review_passed: %v", err)
	}
	if !passed {
		t.Fatal("pass verdict did not write review_passed")
	}
}

func TestReviewStalledStopsAutomation(t *testing.T) {
	reviewAgentID := enableReviewSettings(t, "")
	devAgentID := createHandlerTestAgent(t, "Review Stalled Dev Agent", []byte("[]"))
	issueID := dbfx.Issue(t, "review stalled", testutil.Cols{
		"status":        "todo",
		"assignee_type": "agent",
		"assignee_id":   devAgentID,
	})
	if w := moveIssueTo(t, issueID, "in_review"); w.Code != http.StatusOK {
		t.Fatalf("move to review: %d %s", w.Code, w.Body.String())
	}

	if w := submitReviewVerdict(t, issueID, reviewAgentID, "stalled", map[string]any{
		"summary":            "上一轮问题没有改动",
		"unchanged_previous": []string{"server/internal/example.go"},
	}); w.Code != http.StatusOK {
		t.Fatalf("stalled verdict: %d %s", w.Code, w.Body.String())
	}
	if status := issueStatus(t, issueID); status != "blocked" {
		t.Fatalf("status after stalled = %q, want blocked", status)
	}
	// 停下时必须留一条 @负责人 的评论，否则用户不知道需要介入。
	var content string
	if err := testPool.QueryRow(context.Background(), `SELECT content FROM comment
		WHERE issue_id = $1 AND author_id = $2 ORDER BY created_at DESC LIMIT 1`, issueID, reviewAgentID).Scan(&content); err != nil {
		t.Fatalf("load stop comment: %v", err)
	}
	if !strings.Contains(content, "mention://member/"+testUserID) {
		t.Fatalf("stop comment does not mention the workspace owner: %s", content)
	}
}
