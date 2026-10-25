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
	// Modification 1. Review starts only after a distinct int candidate passes integration.
	dbfx.Exec(t, `UPDATE issue_delivery SET phase='preparing_release',candidate_id='review-int',acceptance_submitted=true,plan_snapshot='{"integration_only":true,"required_checks":[{"name":"verify","stage":"verification"},{"name":"int","stage":"integration"}]}'::jsonb,evidence='[{"name":"int","status":"passed","evidence":"int fixture"}]'::jsonb WHERE id=$1`, delivery)
	dbfx.Cleanup(t, `DELETE FROM delivery_publication WHERE delivery_id=$1`, delivery)
	var lease struct {
		Generation int64 `json:"generation"`
	}
	testutil.Call(t, testHandler.PublicationLease, newRequest("POST", "/publication", map[string]any{"action": "claim", "delivery_id": delivery, "worker_token": token})).Want(200).JSON(&lease)
	dbfx.Exec(t, `UPDATE issue SET status='in_progress', metadata='{"review_passed":true}'::jsonb WHERE id=$1`, issue)
	cleanupIssueTasks(t, issue)

	req := newRequest("POST", "/api/delivery-queue/advance", map[string]any{
		"id":                     delivery,
		"revision":               deliveryRevision(t, delivery),
		"action":                 "integrated",
		"candidate_id":           "review-int",
		"publication_generation": lease.Generation,
		"worker_token":           token,
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
	ranIssue := dbfx.Issue(t, "sweep round ran without verdict", testutil.Cols{
		"status": "in_review", "assignee_type": "agent", "assignee_id": devAgent, "updated_at": old,
	})
	cleanupIssueTasks(t, missed, passedIssue, stoppedIssue, activeIssue, freshIssue, ranIssue)

	seedReviewState(t, passedIssue, reviewer, review.StatePassed, 2, "")
	seedReviewState(t, stoppedIssue, reviewer, review.StateStopped, 2, "")
	seedReviewState(t, activeIssue, reviewer, review.StateDispatched, 1, "")
	dbfx.Task(t, reviewer, testutil.Cols{"issue_id": activeIssue, "runtime_id": handlerTestRuntimeID(t), "status": "running"})
	// 这一轮审查已经跑完但没有提交结论：没有 PR 时无法证明有改动，补扫不能把同一轮再
	// 跑一次，否则轮次不增长会变成无限重审。
	seedReviewState(t, ranIssue, reviewer, review.StateDispatched, 2, "")
	dbfx.Task(t, reviewer, testutil.Cols{"issue_id": ranIssue, "runtime_id": handlerTestRuntimeID(t), "status": "completed"})
	dbfx.Exec(t, `UPDATE agent_task_queue SET created_at=now() WHERE issue_id=$1 AND agent_id=$2`, ranIssue, reviewer)

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
	if got := reviewTasksFor(t, ranIssue, reviewer); got != 1 {
		t.Fatalf("没交结论的那一轮被补扫重跑了，审查任务数变成 %d", got)
	}
	// 直接核对判定本身：读不出运行记录时补扫会按「跳过」处理，只看任务数无法区分
	// 「正确地跳过」和「查询出错后跳过」。
	cfg, configured, err := testHandler.loadReviewConfig(context.Background(), parseUUID(testWorkspaceID))
	if err != nil || !configured {
		t.Fatalf("load review config: configured=%v err=%v", configured, err)
	}
	ranRow, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(ranIssue))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	reason, err := testHandler.reviewSweepSkipReason(context.Background(), ranRow, cfg)
	if err != nil {
		t.Fatalf("检查已跑未交结论的任务出错: %v", err)
	}
	if reason != "本轮审查已执行但未提交结论，等待人工处理" {
		t.Fatalf("已跑未交结论的跳过原因 = %q", reason)
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
		{name: "派发丢失，从来没有审查运行", edit: func(c *reviewSweepCandidate) {
			c.State = review.StateDispatched
		}, want: ""},
		{name: "本轮审查执行过但没交结论", edit: func(c *reviewSweepCandidate) {
			c.State = review.StateDispatched
			c.RanThisRound = true
		}, want: "本轮审查已执行但未提交结论，等待人工处理"},
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
