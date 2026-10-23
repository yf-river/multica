package handler

import (
	"encoding/json"
	"strings"
	"testing"

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
