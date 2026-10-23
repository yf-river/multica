package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/review"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// 补扫的边界：交付入口漏掉的派发、入队失败留下的空轮次都要自愈，已通过、已停止和正在
// 跑的审查不能被再派一次。

func reviewTasksFor(t *testing.T, issueID, agentID string) int {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue
		WHERE issue_id=$1 AND agent_id=$2`, issueID, agentID).Scan(&count); err != nil {
		t.Fatalf("count review tasks: %v", err)
	}
	return count
}

// deliveryRevision 读当前交付记录的 revision，避免测试自己猜乐观锁值。
func deliveryRevision(t *testing.T, deliveryID string) int64 {
	t.Helper()
	var revision int64
	if err := testPool.QueryRow(context.Background(), `SELECT revision FROM issue_delivery WHERE id=$1`, deliveryID).Scan(&revision); err != nil {
		t.Fatalf("load delivery revision: %v", err)
	}
	return revision
}

func cleanupIssueTasks(t *testing.T, issueIDs ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, issueID := range issueIDs {
			testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id=$1`, issueID)
		}
	})
}

// seedReviewState 写一行既有审查记录，updated_at 默认推到宽限期之外。
func seedReviewState(t *testing.T, issueID, reviewer, state string, round int, headSHA string) {
	t.Helper()
	dbfx.InsertNoID(t, "issue_review", testutil.Cols{
		"issue_id":     issueID,
		"workspace_id": testWorkspaceID,
		"agent_id":     reviewer,
		"round":        round,
		"state":        state,
		"head_sha":     headSHA,
	}, "issue_id=$1", issueID)
	dbfx.Exec(t, `UPDATE issue_review SET updated_at=now()-interval '5 minutes' WHERE issue_id=$1`, issueID)
}

// TestDeliveryAcceptanceDispatchesReview 覆盖原缺陷：交付验收通过也要派发审查，并清掉
// 上一轮的绿勾。
func TestDeliveryAcceptanceDispatchesReview(t *testing.T) {
	reviewer := enableReviewSettings(t, "")
	issue, plan := coordinationFixture(t)
	delivery, token := coordinationDelivery(t, issue, plan, "testing")
	dbfx.Exec(t, `UPDATE issue_delivery SET acceptance_submitted=true WHERE id=$1`, delivery)
	dbfx.Exec(t, `UPDATE issue SET status='in_progress', metadata='{"review_passed":true}'::jsonb WHERE id=$1`, issue)
	cleanupIssueTasks(t, issue)

	req := newRequest("POST", "/api/delivery-queue/advance", map[string]any{
		"id":           delivery,
		"revision":     deliveryRevision(t, delivery),
		"action":       "accepted",
		"worker_token": token,
	})
	req = withChatTestWorkspaceCtx(t, req)
	testutil.Call(t, testHandler.AdvanceDelivery, req).Want(http.StatusOK)

	if status := issueStatus(t, issue); status != "in_review" {
		t.Fatalf("交付验收后状态 = %q, want in_review", status)
	}
	if got := reviewTasksFor(t, issue, reviewer); got != 1 {
		t.Fatalf("交付验收后审查任务数 = %d, want 1", got)
	}
	// 新入口派出的任务要和手动入口一样带上轮次与临时说明，否则审查员拿不到本轮上下文。
	if handoff := reviewTaskHandoff(t, issue, reviewer); !strings.Contains(handoff, "第 1 轮") {
		t.Fatalf("交付验收派出的审查缺少本轮说明: %s", handoff)
	}
	var round int
	var state string
	if err := testPool.QueryRow(context.Background(), `SELECT round, state FROM issue_review
		WHERE issue_id=$1`, issue).Scan(&round, &state); err != nil {
		t.Fatalf("load review state: %v", err)
	}
	if round != 1 || state != review.StateDispatched {
		t.Fatalf("审查轮次 = %d/%s, want 1/%s", round, state, review.StateDispatched)
	}
	var passed bool
	if err := testPool.QueryRow(context.Background(), `SELECT COALESCE((metadata->>'review_passed')::boolean,false)
		FROM issue WHERE id=$1`, issue).Scan(&passed); err != nil {
		t.Fatalf("load review_passed: %v", err)
	}
	if passed {
		t.Fatal("重新进入审核中后上一轮绿勾没有清除")
	}
}

// TestReviewSweeperOnlyFillsMissedRounds 覆盖补扫的四个分支：漏派的自愈，已通过、已停止、
// 正在跑的一律不动。
func TestReviewSweeperOnlyFillsMissedRounds(t *testing.T) {
	reviewer := enableReviewSettings(t, "")
	devAgent := createHandlerTestAgent(t, "Sweep Dev Agent", []byte("[]"))
	old := testutil.Raw("now()-interval '5 minutes'")

	missed := dbfx.Issue(t, "sweep missed dispatch", testutil.Cols{
		"status": "in_review", "assignee_type": "agent", "assignee_id": devAgent, "updated_at": old,
	})
	passedIssue := dbfx.Issue(t, "sweep passed", testutil.Cols{
		"status": "in_review", "assignee_type": "agent", "assignee_id": devAgent, "updated_at": old,
		"metadata": testutil.Raw(`'{"review_passed":true}'::jsonb`),
	})
	stoppedIssue := dbfx.Issue(t, "sweep stopped", testutil.Cols{
		"status": "in_review", "assignee_type": "agent", "assignee_id": devAgent, "updated_at": old,
	})
	activeIssue := dbfx.Issue(t, "sweep active", testutil.Cols{
		"status": "in_review", "assignee_type": "agent", "assignee_id": devAgent, "updated_at": old,
	})
	freshIssue := dbfx.Issue(t, "sweep just changed", testutil.Cols{
		"status": "in_review", "assignee_type": "agent", "assignee_id": devAgent,
	})
	cleanupIssueTasks(t, missed, passedIssue, stoppedIssue, activeIssue, freshIssue)

	seedReviewState(t, passedIssue, reviewer, review.StatePassed, 2, "")
	seedReviewState(t, stoppedIssue, reviewer, review.StateStopped, 2, "")
	seedReviewState(t, activeIssue, reviewer, review.StateDispatched, 1, "")
	dbfx.Task(t, reviewer, testutil.Cols{"issue_id": activeIssue, "runtime_id": handlerTestRuntimeID(t), "status": "running"})

	stats := testHandler.SweepIssueReviews(context.Background())
	if stats.Dispatched < 1 {
		t.Fatalf("补扫没有派发漏掉的任务：%+v", stats)
	}

	if got := reviewTasksFor(t, missed, reviewer); got != 1 {
		t.Fatalf("漏派任务收到 %d 个审查任务, want 1", got)
	}
	if got := reviewTasksFor(t, passedIssue, reviewer); got != 0 {
		t.Fatalf("已通过任务被重新派发 %d 次", got)
	}
	if got := reviewTasksFor(t, stoppedIssue, reviewer); got != 0 {
		t.Fatalf("人工停止的任务被重新派发 %d 次", got)
	}
	if got := reviewTasksFor(t, activeIssue, reviewer); got != 1 {
		t.Fatalf("正在跑的审查变成 %d 个任务, want 1（不重复派发）", got)
	}
	if got := reviewTasksFor(t, freshIssue, reviewer); got != 0 {
		t.Fatalf("刚进入审核中的任务被补扫抢先派发 %d 次", got)
	}

	var passedState string
	var passedRound int
	if err := testPool.QueryRow(context.Background(), `SELECT state, round FROM issue_review
		WHERE issue_id=$1`, passedIssue).Scan(&passedState, &passedRound); err != nil {
		t.Fatalf("load passed review: %v", err)
	}
	if passedState != review.StatePassed || passedRound != 2 {
		t.Fatalf("已通过结果被改写为 %s/%d", passedState, passedRound)
	}
}

// TestReviewSweepSkipReasonBoundaries 固定补扫的判定边界：打回后提交未变就不再来回审查，
// 有新提交才放行。
func TestReviewSweepSkipReasonBoundaries(t *testing.T) {
	base := reviewSweepCandidate{
		HasRow:      true,
		StatusAge:   5 * time.Minute,
		ReviewAge:   5 * time.Minute,
		CurrentHead: strings.Repeat("a", 40),
	}
	cases := []struct {
		name string
		edit func(*reviewSweepCandidate)
		want string
	}{
		{name: "从未审查", edit: func(c *reviewSweepCandidate) { c.HasRow = false }, want: ""},
		{name: "打回且提交未变", edit: func(c *reviewSweepCandidate) {
			c.State = review.StateRejected
			c.ReviewedHead = c.CurrentHead
		}, want: "打回后没有新提交"},
		{name: "打回后有新提交", edit: func(c *reviewSweepCandidate) {
			c.State = review.StateRejected
			c.ReviewedHead = strings.Repeat("b", 40)
		}, want: ""},
		{name: "已通过当前候选", edit: func(c *reviewSweepCandidate) {
			c.State = review.StatePassed
			c.Passed = true
			c.ReviewedHead = c.CurrentHead
		}, want: "已通过当前候选"},
		{name: "通过后又改了代码", edit: func(c *reviewSweepCandidate) {
			c.State = review.StatePassed
			c.Passed = true
			c.ReviewedHead = strings.Repeat("b", 40)
		}, want: ""},
		{name: "通过标记丢失", edit: func(c *reviewSweepCandidate) {
			c.State = review.StatePassed
			c.ReviewedHead = c.CurrentHead
		}, want: ""},
		{name: "人工停止", edit: func(c *reviewSweepCandidate) { c.State = review.StateStopped }, want: "自动返工已停止，等待人工处理"},
		{name: "已有活动运行", edit: func(c *reviewSweepCandidate) { c.ActiveRun = true }, want: "已有活动审查运行"},
		{name: "刚派发", edit: func(c *reviewSweepCandidate) {
			c.State = review.StateDispatched
			c.ReviewAge = 5 * time.Second
		}, want: "刚派发，等待运行"},
		{name: "任务刚变化", edit: func(c *reviewSweepCandidate) { c.StatusAge = 5 * time.Second }, want: "任务刚变化"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			tc.edit(&candidate)
			if got := candidate.skipReason(); got != tc.want {
				t.Fatalf("skipReason = %q, want %q", got, tc.want)
			}
		})
	}
}
