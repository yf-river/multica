package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// 通知文案必须说明为什么暂停、授权依据在哪里，以及唯一的继续路径；不能让负责人重新向用户索取已给出的授权。
func TestDeliveryReviewNoticeExplainsContinuation(t *testing.T) {
	for _, want := range []string{
		"用户已授权发布",
		"尚未复核当前候选",
		"authorization_id",
		"scope_review",
		"user_comments_version",
		"不要再让用户重复确认",
		"撤回授权时不要继续发布",
	} {
		if !strings.Contains(deliveryReviewNotice, want) {
			t.Errorf("授权复核通知缺少 %q: %s", want, deliveryReviewNotice)
		}
	}
	if strings.Contains(deliveryReviewNotice, "标记任务完成") || strings.Contains(deliveryReviewNotice, "已上线") {
		t.Errorf("复核通知不能让负责人把未发布交付当作完成: %s", deliveryReviewNotice)
	}
}

// deliveryReviewFixture 造一份 int 验收完成、等待发布复核的交付，并按需挂上有效发布授权。
func deliveryReviewFixture(t *testing.T, withAuthorization bool) (issue, delivery, token, authorization string) {
	t.Helper()
	issue, plan := coordinationFixture(t)
	if withAuthorization {
		comment := dbfx.Insert(t, "comment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issue, "author_type": "member", "author_id": testUserID, "content": "测试验收通过后直接发布"})
		authorization = dbfx.Insert(t, "issue_delivery_authorization", testutil.Cols{"id": dbid.NewV7(), "workspace_id": testWorkspaceID, "issue_id": issue, "plan_id": plan, "kind": "release", "comment_id": comment, "comment_revision": 1, "comment_snapshot": testutil.Raw(`jsonb_build_object('author_id','` + testUserID + `')`), "scope": "本次改动 int 验收通过后发布生产", "registered_by_type": "member", "registered_by": testUserID})
	}
	delivery, token = coordinationDelivery(t, issue, plan, "testing")
	dbfx.Exec(t, `UPDATE issue_delivery SET candidate_id='candidate-1',candidate_url='http://int.example',acceptance_submitted=true WHERE id=$1`, delivery)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id=$1`, issue)
	dbfx.Cleanup(t, `DELETE FROM comment WHERE issue_id=$1`, issue)
	return issue, delivery, token, authorization
}

func advanceAccepted(t *testing.T, delivery, token string) {
	t.Helper()
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{"id": delivery, "revision": 1, "worker_token": token, "action": "accepted"})).Want(200)
}

func deliveryReviewNoticeCount(t *testing.T, delivery string) int {
	t.Helper()
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM comment c JOIN issue_delivery d ON d.issue_id=c.issue_id WHERE d.id=$1 AND c.content LIKE '%尚未复核当前候选%'`, delivery).Scan(&count)
	return count
}

func deliveryReviewRow(t *testing.T, issue, delivery string) (phase, pending, detail, state string) {
	t.Helper()
	dbfx.QueryRow(t, `SELECT phase,COALESCE(plan_snapshot->>'authorization_pending',''),error FROM issue_delivery WHERE id=$1`, delivery).Scan(&phase, &pending, &detail)
	var body struct {
		Deliveries []struct {
			ID                 string `json:"id"`
			AuthorizationState string `json:"authorization_state"`
		} `json:"deliveries"`
	}
	testutil.Call(t, testHandler.GetIssueDeliveries, withURLParam(newRequest("GET", "/deliveries", nil), "id", issue)).Want(200).JSON(&body)
	for _, d := range body.Deliveries {
		if d.ID == delivery {
			state = d.AuthorizationState
		}
	}
	return phase, pending, detail, state
}

// 已有有效授权但未复核当前候选时，int 验收结束后要落库待复核意图并定向通知负责人。
func TestAcceptedDeliveryContinuesExistingReleaseAuthorization(t *testing.T) {
	issue, delivery, token, authorization := deliveryReviewFixture(t, true)
	advanceAccepted(t, delivery, token)
	phase, pending, detail, state := deliveryReviewRow(t, issue, delivery)
	if phase != "awaiting_review" {
		t.Fatalf("阶段应为 awaiting_review，实际 %s", phase)
	}
	if pending != "" {
		t.Fatalf("通知送达后应清掉待通知标记，实际 %s", pending)
	}
	if !strings.Contains(detail, "已有发布授权") {
		t.Fatalf("缺少等待负责人复核的原因: %s", detail)
	}
	if state != "review_incomplete" {
		t.Fatalf("已通知待接续的交付应显示 review_incomplete，实际 %s", state)
	}
	if count := deliveryReviewNoticeCount(t, delivery); count != 1 {
		t.Fatalf("应只发送一次复核通知，实际 %d", count)
	}
	var raw []byte
	dbfx.QueryRow(t, `SELECT plan_snapshot->'authorization_notice' FROM issue_delivery WHERE id=$1`, delivery).Scan(&raw)
	var notice deliveryAuthorizationNotice
	if err := json.Unmarshal(raw, &notice); err != nil {
		t.Fatalf("待复核意图无法解析: %v (%s)", err, raw)
	}
	if notice.CandidateID != "candidate-1" || notice.AuthorizationID != authorization || notice.CommentID == "" {
		t.Fatalf("待复核意图没有绑定当前候选和授权: %+v", notice)
	}
}

// 没有有效授权时只记录等待原因，不自动批准也不发送复核通知。
func TestAcceptedDeliveryWaitsForUserAuthorization(t *testing.T) {
	issue, delivery, token, _ := deliveryReviewFixture(t, false)
	advanceAccepted(t, delivery, token)
	phase, pending, detail, state := deliveryReviewRow(t, issue, delivery)
	if phase != "awaiting_review" || pending != "" {
		t.Fatalf("无授权时不应落库待复核意图: %s %s", phase, pending)
	}
	if !strings.Contains(detail, "等待用户发布授权") {
		t.Fatalf("缺少等待用户授权的原因: %s", detail)
	}
	if state != "user_authorization" {
		t.Fatalf("等待用户授权应单独可见，实际 %s", state)
	}
	if count := deliveryReviewNoticeCount(t, delivery); count != 0 {
		t.Fatal("无授权时不应通知负责人复核")
	}
}

// 授权已绑定当前候选时直接推进发布，不再要求用户确认。
func TestAcceptedDeliveryAdvancesWhenCandidateAlreadyReviewed(t *testing.T) {
	issue, delivery, token, authorization := deliveryReviewFixture(t, true)
	var version string
	dbfx.QueryRow(t, `SELECT md5(COALESCE(string_agg(id::text||':'||revision::text,',' ORDER BY id),'')) FROM comment WHERE workspace_id=$1 AND issue_id=$2 AND author_type='member'`, testWorkspaceID, issue).Scan(&version)
	review, _ := json.Marshal(map[string]string{"authorization_id": authorization, "scope": "本次改动", "comments_version": version, "source_commit": strings.Repeat("a", 40), "candidate_id": "candidate-1"})
	dbfx.Exec(t, `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{authorization_review}',$2::jsonb) WHERE id=$1`, delivery, string(review))
	advanceAccepted(t, delivery, token)
	phase, pending, detail, _ := deliveryReviewRow(t, issue, delivery)
	if phase != "approved" {
		t.Fatalf("已复核当前候选应直接进入 approved，实际 %s（%s）", phase, detail)
	}
	if pending != "" || deliveryReviewNoticeCount(t, delivery) != 0 {
		t.Fatal("已复核的候选不应再触发复核通知")
	}
}

// 负责人已有活动运行时先保留待通知意图，运行结束后再补发，且只补发一次。
func TestDeliveryReviewNotificationDeduplicates(t *testing.T) {
	issue, delivery, token, _ := deliveryReviewFixture(t, true)
	var agent string
	dbfx.QueryRow(t, `SELECT id FROM agent WHERE workspace_id=$1 AND name='Handler Test Agent'`, testWorkspaceID).Scan(&agent)
	if agent == "" {
		t.Fatal("缺少测试 Agent")
	}
	dbfx.Exec(t, `UPDATE issue SET assignee_type='agent',assignee_id=$2 WHERE id=$1`, issue, agent)
	running := dbfx.Insert(t, "agent_task_queue", testutil.Cols{"agent_id": agent, "issue_id": issue, "status": "running", "runtime_id": handlerTestRuntimeID(t)})
	advanceAccepted(t, delivery, token)
	phase, pending, detail, _ := deliveryReviewRow(t, issue, delivery)
	if phase != "awaiting_review" || pending != "true" || deliveryReviewNoticeCount(t, delivery) != 0 {
		t.Fatalf("负责人已有活动运行时不应另起一轮: %s %s %d (%s)", phase, pending, deliveryReviewNoticeCount(t, delivery), detail)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1`, running)
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{"id": delivery, "revision": 2, "worker_token": token, "action": "notify-authorization"})).Want(200)
	_, pending, _, _ = deliveryReviewRow(t, issue, delivery)
	if pending != "" || deliveryReviewNoticeCount(t, delivery) != 1 {
		t.Fatalf("运行结束后应补发一次通知并清掉待通知标记: %s %d", pending, deliveryReviewNoticeCount(t, delivery))
	}
	// 上一次复核运行还在队列里时不得再唤醒一轮。
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{"id": delivery, "revision": 2, "worker_token": token, "action": "notify-authorization"})).Want(409)
	if count := deliveryReviewNoticeCount(t, delivery); count != 1 {
		t.Fatalf("复核运行未结束时不得重复唤醒，实际 %d", count)
	}
}

// 撤回授权后按无授权处理，不能再自动推进。
func TestRevokedAuthorizationStopsAutomaticReview(t *testing.T) {
	issue, delivery, token, _ := deliveryReviewFixture(t, true)
	dbfx.Exec(t, `UPDATE issue_delivery_authorization SET revoked=true WHERE issue_id=$1`, issue)
	advanceAccepted(t, delivery, token)
	phase, pending, detail, state := deliveryReviewRow(t, issue, delivery)
	if phase != "awaiting_review" || pending != "" || deliveryReviewNoticeCount(t, delivery) != 0 {
		t.Fatalf("授权撤回后不应继续复核: %s %s %d", phase, pending, deliveryReviewNoticeCount(t, delivery))
	}
	if !strings.Contains(detail, "等待用户发布授权") || state != "user_authorization" {
		t.Fatalf("撤回后应说明等待用户授权: %s %s", detail, state)
	}
}

func deliveryRevision(t *testing.T, delivery string) int64 {
	t.Helper()
	var rev int64
	dbfx.QueryRow(t, `SELECT revision FROM issue_delivery WHERE id=$1`, delivery).Scan(&rev)
	return rev
}

func advanceNotifyAuthorization(t *testing.T, delivery string, want int) *testutil.Response {
	t.Helper()
	return testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{"id": delivery, "revision": deliveryRevision(t, delivery), "action": "notify-authorization"})).Want(want)
}

// 1. 显式接续有次数上限：补发到上限后保留原意图并说明原因，重复调用不再补发，
// 也不能清掉意图重新从零计数。
func TestDeliveryReviewResendStopsAtLimit(t *testing.T) {
	issue, delivery, token, _ := deliveryReviewFixture(t, true)
	advanceAccepted(t, delivery, token)
	if first := deliveryReviewNoticeCount(t, delivery); first != 1 {
		t.Fatalf("首次应只通知一次，实际 %d", first)
	}
	for i := 0; i < deliveryReviewRewakeLimit; i++ {
		advanceNotifyAuthorization(t, delivery, 200)
	}
	if got := deliveryReviewNoticeCount(t, delivery); got != 1+deliveryReviewRewakeLimit {
		t.Fatalf("补发次数应为 %d，实际通知 %d 条", deliveryReviewRewakeLimit, got)
	}
	advanceNotifyAuthorization(t, delivery, 409)
	advanceNotifyAuthorization(t, delivery, 409)
	if got := deliveryReviewNoticeCount(t, delivery); got != 1+deliveryReviewRewakeLimit {
		t.Fatalf("超限后不得继续补发，实际通知 %d 条", got)
	}
	var attempts int
	var stalled bool
	dbfx.QueryRow(t, `SELECT COALESCE((plan_snapshot->'authorization_notice'->>'attempts')::int,0),COALESCE((plan_snapshot->>'authorization_stalled')::boolean,false) FROM issue_delivery WHERE id=$1`, delivery).Scan(&attempts, &stalled)
	if attempts != deliveryReviewRewakeLimit || !stalled {
		t.Fatalf("超限后应保留次数并标记停止: attempts=%d stalled=%v", attempts, stalled)
	}
	phase, pending, detail, state := deliveryReviewRow(t, issue, delivery)
	if phase != "awaiting_review" || pending != "" || state != "review_incomplete" {
		t.Fatalf("超限后交付状态应保持可见: %s %s %s", phase, pending, state)
	}
	if !strings.Contains(detail, "负责人复核未接续") {
		t.Fatalf("超限后应保留具体原因: %s", detail)
	}
}

// 2. 通知写入失败时保留待通知意图，恢复后由队列补发一次；不能因为一次失败丢工作。
func TestDeliveryReviewNotificationFailureKeepsIntent(t *testing.T) {
	issue, delivery, token, _ := deliveryReviewFixture(t, true)
	dbfx.Exec(t, `CREATE OR REPLACE FUNCTION gong68_block_comment() RETURNS trigger LANGUAGE plpgsql AS $body$ BEGIN RAISE EXCEPTION '通知写入失败'; END $body$`)
	dbfx.Exec(t, `CREATE TRIGGER gong68_block_comment BEFORE INSERT ON comment FOR EACH ROW EXECUTE FUNCTION gong68_block_comment()`)
	drop := func() {
		dbfx.Exec(t, `DROP TRIGGER IF EXISTS gong68_block_comment ON comment`)
		dbfx.Exec(t, `DROP FUNCTION IF EXISTS gong68_block_comment()`)
	}
	t.Cleanup(drop)
	advanceAccepted(t, delivery, token)
	phase, pending, detail, state := deliveryReviewRow(t, issue, delivery)
	if phase != "awaiting_review" || pending != "true" || state != "owner_review" {
		t.Fatalf("写入失败后应保留待通知意图: %s %s %s (%s)", phase, pending, state, detail)
	}
	if deliveryReviewNoticeCount(t, delivery) != 0 {
		t.Fatal("写入失败时不应该留下半条通知")
	}
	drop()
	advanceNotifyAuthorization(t, delivery, 200)
	if _, pending, _, _ = deliveryReviewRow(t, issue, delivery); pending != "" {
		t.Fatalf("恢复后应清掉待通知标记: %s", pending)
	}
	if got := deliveryReviewNoticeCount(t, delivery); got != 1 {
		t.Fatalf("恢复后应恰好补发一次，实际 %d", got)
	}
}

// 3. 服务重启不依赖内存状态：已落库的意图由新进程继续发送；“已写入通知但未清标记”
// 的重启窗口也不会重复通知。
func TestDeliveryReviewNotificationResumesAfterRestart(t *testing.T) {
	issue, delivery, token, _ := deliveryReviewFixture(t, true)
	var agent string
	dbfx.QueryRow(t, `SELECT id FROM agent WHERE workspace_id=$1 AND name='Handler Test Agent'`, testWorkspaceID).Scan(&agent)
	if agent == "" {
		t.Fatal("缺少测试 Agent")
	}
	dbfx.Exec(t, `UPDATE issue SET assignee_type='agent',assignee_id=$2 WHERE id=$1`, issue, agent)
	running := dbfx.Insert(t, "agent_task_queue", testutil.Cols{"agent_id": agent, "issue_id": issue, "status": "running", "runtime_id": handlerTestRuntimeID(t)})
	advanceAccepted(t, delivery, token)
	if _, pending, _, _ := deliveryReviewRow(t, issue, delivery); pending != "true" {
		t.Fatal("负责人运行期间应先落库待通知意图")
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1`, running)
	restarted := New(testHandler.Queries, testPool, testHandler.Hub, testHandler.Bus, testHandler.EmailService, nil, nil, analytics.NoopClient{}, Config{AllowSignup: true})
	testutil.Call(t, restarted.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{"id": delivery, "revision": deliveryRevision(t, delivery), "action": "notify-authorization"})).Want(200)
	if _, pending, _, _ := deliveryReviewRow(t, issue, delivery); pending != "" {
		t.Fatal("重启接续后应清掉待通知标记")
	}
	if got := deliveryReviewNoticeCount(t, delivery); got != 1 {
		t.Fatalf("重启后应恰好发送一次，实际 %d", got)
	}
	// 模拟“通知已写入、标记未清”时进程退出：重启后只清标记，不再补发。
	dbfx.Exec(t, `UPDATE issue_delivery SET plan_snapshot=plan_snapshot||'{"authorization_pending":true}'::jsonb WHERE id=$1`, delivery)
	testutil.Call(t, restarted.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{"id": delivery, "revision": deliveryRevision(t, delivery), "action": "notify-authorization"})).Want(200)
	if got := deliveryReviewNoticeCount(t, delivery); got != 1 {
		t.Fatalf("重启窗口不应重复通知，实际 %d", got)
	}
	if _, pending, _, _ := deliveryReviewRow(t, issue, delivery); pending != "" {
		t.Fatal("重启窗口恢复后应清掉待通知标记")
	}
}
