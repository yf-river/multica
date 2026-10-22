package delivery_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/middleware"
	fxt "github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func TestFixedIntDeliveryWorkflow(t *testing.T) {
	raw := os.Getenv("MULTICA_DELIVERY_TEST_URL")
	if raw == "" {
		t.Skip("requires isolated verification_full_test database")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Host != "127.0.0.1" || cfg.ConnConfig.Database != "verification_full_test" {
		t.Fatal("refusing non-test target")
	}
	base, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	schema := fmt.Sprintf("delivery_test_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		for _, table := range []string{"verification_event", "verification_slot", "issue_delivery_authorization", "issue_delivery", "issue_delivery_plan"} {
			if _, e := base.Exec(ctx, "DROP TABLE IF EXISTS "+schema+"."+table); e != nil {
				t.Error(e)
			}
		}
		if _, e := base.Exec(ctx, "DROP SCHEMA "+schema); e != nil {
			t.Error(e)
		}
	})
	for _, pattern := range []string{"542_*.up.sql", "543_*.up.sql", "544_*.up.sql", "545_*.up.sql", "546_*.up.sql", "547_*.up.sql", "548_*.up.sql", "549_*.up.sql", "567_*.up.sql", "568_*.up.sql", "569_*.up.sql"} {
		names, _ := filepath.Glob("../../migrations/" + pattern)
		if len(names) != 1 {
			t.Fatal(pattern)
		}
		sql, _ := os.ReadFile(names[0])
		if _, err = pool.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	f := fxt.New(pool, "", "")
	f.UserID = f.User(t, "交付验收", schema+"@example.test", fxt.Cols{"account": schema})
	f.WorkspaceID = f.Workspace(t, "交付验收", schema)
	f.Member(t, f.WorkspaceID, f.UserID, "owner")
	issue := f.Issue(t, "交付验收目标")
	t.Cleanup(func() {
		for _, table := range []string{"supervision_material", "supervision_subject", "domain_event_outbox"} {
			_, _ = pool.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1", f.WorkspaceID)
		}
	})
	thread := f.Insert(t, "comment", fxt.Cols{"issue_id": issue, "workspace_id": f.WorkspaceID, "author_type": "member", "author_id": f.UserID, "content": "旧讨论入口"})
	request := f.Insert(t, "comment", fxt.Cols{"issue_id": issue, "workspace_id": f.WorkspaceID, "author_type": "member", "author_id": f.UserID, "parent_id": thread, "content": "重新联调"})
	comment := f.Insert(t, "comment", fxt.Cols{"issue_id": issue, "workspace_id": f.WorkspaceID, "author_type": "member", "author_id": f.UserID, "parent_id": request, "content": "1. 修改链接。2. 联调验证。\n完整计划：生产需单独批准。"})
	confirmation := f.Insert(t, "comment", fxt.Cols{"issue_id": issue, "workspace_id": f.WorkspaceID, "author_type": "member", "author_id": f.UserID, "parent_id": comment, "content": "确认计划"})
	h := &handler.Handler{DB: pool, Queries: db.New(pool), TxStarter: pool}
	call := func(fn http.HandlerFunc, agent bool, body any, params ...string) (int, map[string]any) {
		req := fxt.WithURLParams(fxt.WithHeaders(fxt.JSONRequest(http.MethodPost, "/", body), "X-User-ID", f.UserID, "X-Workspace-ID", f.WorkspaceID), params...)
		req = req.WithContext(middleware.SetMemberContext(req.Context(), f.WorkspaceID, db.Member{}))
		if agent {
			req.Header.Set("X-Actor-Source", "task_token")
			req.Header.Set("X-Agent-ID", dbid.NewV7().String())
		}
		res := fxt.Call(t, fn, req)
		var value map[string]any
		_ = json.Unmarshal(res.Body.Bytes(), &value)
		return res.Code, value
	}
	want := func(code int, want int, v map[string]any) {
		t.Helper()
		if code != want {
			t.Fatalf("want %d got %d: %v", want, code, v)
		}
	}
	code, s := call(h.RegisterIssuePlan, true, map[string]any{"comment_id": comment, "revision": 1}, "id", issue)
	want(code, 200, s)
	plan := s["plan"].(map[string]any)
	code, s = call(h.ConfirmIssuePlan, true, map[string]any{"plan_id": plan["id"], "revision": 1}, "id", issue)
	want(code, 403, s)
	t.Run("thread-confirmation-boundaries", func(t *testing.T) {
		otherThread := f.Insert(t, "comment", fxt.Cols{"issue_id": issue, "workspace_id": f.WorkspaceID, "author_type": "member", "author_id": f.UserID, "content": "其他问题"})
		otherBranch := f.Insert(t, "comment", fxt.Cols{"issue_id": issue, "workspace_id": f.WorkspaceID, "author_type": "member", "author_id": f.UserID, "parent_id": thread, "content": "无关分支问题"})
		otherIssue := f.Issue(t, "其他任务")
		for _, tc := range []struct {
			name, parent, target, author, content string
			old                                   bool
			want                                  int
		}{
			{"same-root", thread, issue, "member", "按这个做", false, 200},
			{"same-parent", request, issue, "member", "同意计划", false, 200},
			{"other-thread", otherThread, issue, "member", "就这么办", false, 200},
			{"other-branch", otherBranch, issue, "member", "开始处理吧", false, 200},
			{"other-issue", thread, otherIssue, "member", "确认", false, 409},
			{"agent", thread, issue, "agent", "确认", false, 409},

			{"prior-authorization", thread, issue, "member", "按当前方案执行", true, 200},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cols := fxt.Cols{"issue_id": tc.target, "workspace_id": f.WorkspaceID, "author_type": tc.author, "author_id": f.UserID, "parent_id": tc.parent, "content": tc.content}
				if tc.old {
					cols["created_at"] = fxt.Raw("now()-interval '1 hour'")
				}
				id := f.Insert(t, "comment", cols)
				c, v := call(h.ConfirmIssuePlan, true, map[string]any{"plan_id": plan["id"], "revision": 1, "comment_id": id, "comment_revision": 1, "scope": "执行当前方案"}, "id", issue)
				want(c, tc.want, v)
			})
		}
	})
	code, s = call(h.ConfirmIssuePlan, true, map[string]any{"plan_id": plan["id"], "revision": 1, "comment_id": confirmation, "comment_revision": 1, "scope": "执行当前方案"}, "id", issue)
	want(code, 200, s)
	checks := []map[string]any{{"name": "真实功能操作", "status": "passed", "evidence": "指定页面及持久化检查通过"}}
	body := map[string]any{"operation_id": dbid.NewV7().String(), "source_commit": strings.Repeat("a", 40), "base_commit": strings.Repeat("b", 40), "source_branch": "feature/test/delivery", "evidence": checks}
	code, s = call(h.SubmitIssueDelivery, true, body, "id", issue)
	want(code, 200, s)
	d := s["deliveries"].([]any)[0].(map[string]any)
	id := d["id"].(string)
	code, s = call(h.SubmitIssueDelivery, true, body, "id", issue)
	want(code, 200, s)
	if len(s["deliveries"].([]any)) != 1 {
		t.Fatal("duplicate submit")
	}
	body["source_commit"] = strings.Repeat("c", 40)
	code, s = call(h.SubmitIssueDelivery, true, body, "id", issue)
	want(code, 409, s)
	revision := int64(1)
	token := ""
	advance := func(action string, extra map[string]any) {
		t.Helper()
		b := map[string]any{"id": id, "revision": revision, "worker_token": token, "action": action}
		for k, v := range extra {
			b[k] = v
		}
		c, v := call(h.AdvanceDelivery, false, b)
		want(c, 200, v)
		revision = int64(v["revision"].(float64))
		token = v["worker_token"].(string)
	}
	advance("claim", nil)
	// 真实数据库上的池化领取、迟到回执与后续 int 发布共用同一交付流程。
	f.Exec(t, `INSERT INTO verification_slot(name,enabled) VALUES('verify-1',true)`)
	code, lease := call(h.AdvanceVerificationSlot, false, map[string]any{"action": "claim", "delivery_id": id, "worker_token": token, "source_commit": strings.Repeat("a", 40)})
	want(code, 200, lease)
	code, s = call(h.AdvanceDelivery, false, map[string]any{"id": id, "revision": revision, "action": "fail", "worker_token": dbid.NewV7().String(), "error": "stale worker"})
	want(code, 403, s)
	code, s = call(h.AdvanceDelivery, false, map[string]any{"id": id, "revision": revision, "action": "prepared", "worker_token": token, "candidate_id": "wrong", "candidate_url": "http://fixture"})
	want(code, 409, s)
	lease["action"] = "prepared"
	lease["candidate_id"] = "candidate-one"
	code, s = call(h.AdvanceVerificationSlot, false, lease)
	want(code, 200, s)
	advance("prepared", map[string]any{"candidate_id": "candidate-one", "candidate_url": "http://int.example.test"})
	action := func(name string, agent bool, extra map[string]any, expected int) {
		t.Helper()
		b := map[string]any{"revision": revision, "action": name}
		for k, v := range extra {
			b[k] = v
		}
		c, v := call(h.ActIssueDelivery, agent, b, "id", issue, "deliveryId", id)
		want(c, expected, v)
		if c == 200 {
			d = v["deliveries"].([]any)[0].(map[string]any)
			revision = int64(d["revision"].(float64))
		}
	}
	action("approve", true, nil, 409)
	action("accept", true, map[string]any{"evidence": []any{}}, 409)
	action("accept", true, map[string]any{"evidence": checks}, 200)
	advance("accepted", nil)
	lease["action"] = "drain"
	code, s = call(h.AdvanceVerificationSlot, false, lease)
	want(code, 200, s)
	lease["action"] = "release"
	code, s = call(h.AdvanceVerificationSlot, false, lease)
	want(code, 200, s)
	var remaining int
	f.QueryRow(t, `SELECT count(*) FROM verification_slot WHERE delivery_id=$1`, id).Scan(&remaining)
	if remaining != 0 {
		t.Fatal("review retained verification slot")
	}
	var status string
	f.QueryRow(t, "SELECT status FROM issue WHERE id=$1", issue).Scan(&status)
	if status != "in_review" {
		t.Fatal(status)
	}
	action("approve", true, nil, 403)
	approval := f.Insert(t, "comment", fxt.Cols{"issue_id": issue, "workspace_id": f.WorkspaceID, "author_type": "member", "author_id": f.UserID, "content": "测完没问题就上线吧"})
	version := func() string {
		var v string
		f.QueryRow(t, `SELECT md5(COALESCE(string_agg(id::text||':'||revision::text,',' ORDER BY id),'')) FROM comment WHERE workspace_id=$1 AND issue_id=$2 AND author_type='member'`, f.WorkspaceID, issue).Scan(&v)
		return v
	}
	action("approve", true, map[string]any{"comment_id": approval, "comment_revision": 2, "scope": "当前方案发布", "scope_review": "改动符合方案", "user_comments_version": version()}, 409)
	action("approve", true, map[string]any{"comment_id": approval, "comment_revision": 1, "scope": "当前方案发布", "scope_review": "改动符合方案", "user_comments_version": version()}, 200)
	authorization := d["plan_snapshot"].(map[string]any)["authorization_review"].(map[string]any)["authorization_id"]

	advance("claim", nil)
	advance("prepared", map[string]any{"candidate_id": "candidate-two", "candidate_url": "http://int.example.test"})
	action("accept", true, map[string]any{"evidence": checks, "authorization_id": authorization, "scope_review": "整合后仍在已批准方案范围", "user_comments_version": version()}, 200)
	advance("validated", nil)
	advance("publishing", nil)
	advance("published", nil)
	f.QueryRow(t, "SELECT status FROM issue WHERE id=$1", issue).Scan(&status)
	if status != "in_review" {
		t.Fatal("published auto-completed issue")
	}
	t.Run("edited-plan-and-new-delivery", func(t *testing.T) {
		f.Exec(t, `UPDATE comment SET revision=2,content='新版计划',updated_at=clock_timestamp() WHERE id=$1`, comment)
		body["operation_id"] = dbid.NewV7().String()
		c, v := call(h.SubmitIssueDelivery, true, body, "id", issue)
		want(c, 409, v)
		c, v = call(h.ConfirmIssuePlan, true, map[string]any{"plan_id": plan["id"], "revision": 1, "comment_id": confirmation, "comment_revision": 1, "scope": "执行当前方案"}, "id", issue)
		want(c, 409, v)
		c, v = call(h.RegisterIssuePlan, true, map[string]any{"comment_id": comment, "revision": 2}, "id", issue)
		want(c, 200, v)
		newPlan := v["plan"].(map[string]any)
		note := f.Insert(t, "comment", fxt.Cols{"issue_id": issue, "workspace_id": f.WorkspaceID, "author_type": "member", "author_id": f.UserID, "parent_id": comment, "content": "/note 同意"})
		c, v = call(h.ConfirmIssuePlan, true, map[string]any{"plan_id": newPlan["id"], "revision": 2, "comment_id": note, "comment_revision": 2, "scope": "执行新版方案"}, "id", issue)
		want(c, 409, v)
		confirm := f.Insert(t, "comment", fxt.Cols{"issue_id": issue, "workspace_id": f.WorkspaceID, "author_type": "member", "author_id": f.UserID, "parent_id": comment, "content": "确认计划"})
		c, v = call(h.ConfirmIssuePlan, true, map[string]any{"plan_id": newPlan["id"], "revision": 2, "comment_id": confirm, "comment_revision": 1, "scope": "执行新版方案"}, "id", issue)
		want(c, 200, v)
		c, v = call(h.SubmitIssueDelivery, true, body, "id", issue)
		want(c, 200, v)
		prior := v["deliveries"].([]any)[0].(map[string]any)["id"].(string)
		body["operation_id"] = dbid.NewV7().String()
		body["source_commit"] = strings.Repeat("d", 40)
		c, v = call(h.SubmitIssueDelivery, true, body, "id", issue)
		want(c, 200, v)
		var previousPhase, savedPlan string
		f.QueryRow(t, `SELECT phase FROM issue_delivery WHERE id=$1`, prior).Scan(&previousPhase)
		if previousPhase != "needs_changes" {
			t.Fatal("new source did not invalidate prior delivery")
		}
		f.QueryRow(t, `SELECT plan_snapshot->>'body' FROM issue_delivery WHERE id=$1`, id).Scan(&savedPlan)
		if savedPlan == "新版计划" || savedPlan == "" {
			t.Fatal("historical plan was overwritten")
		}
	})

	t.Run("conditional-release-and-new-user-decision", func(t *testing.T) {
		issue = f.Issue(t, "提前授权与新要求")
		planComment := f.Insert(t, "comment", fxt.Cols{"issue_id": issue, "workspace_id": f.WorkspaceID, "author_type": "member", "author_id": f.UserID, "content": "当前方案：修复菜单"})
		grant := f.Insert(t, "comment", fxt.Cols{"issue_id": issue, "workspace_id": f.WorkspaceID, "author_type": "member", "author_id": f.UserID, "content": "照这个处理，测好没问题直接上线"})
		c, v := call(h.RegisterIssuePlan, true, map[string]any{"comment_id": planComment, "revision": 1}, "id", issue)
		want(c, 200, v)
		planID := v["plan"].(map[string]any)["id"]
		confirmationBody := map[string]any{"plan_id": planID, "revision": 1, "comment_id": grant, "comment_revision": 1, "scope": "修复菜单并在验收通过后发布", "release_after_acceptance": true}
		c, v = call(h.ConfirmIssuePlan, true, confirmationBody, "id", issue)
		want(c, 200, v)
		c, v = call(h.ConfirmIssuePlan, true, confirmationBody, "id", issue)
		want(c, 200, v)
		if len(v["authorizations"].([]any)) != 2 {
			t.Fatal("授权重复登记")
		}
		authorization := ""
		for _, a := range v["authorizations"].([]any) {
			r := a.(map[string]any)
			if r["kind"] == "release" {
				authorization = r["id"].(string)
			}
		}
		body["operation_id"] = dbid.NewV7().String()
		c, v = call(h.SubmitIssueDelivery, true, body, "id", issue)
		want(c, 200, v)
		d = v["deliveries"].([]any)[0].(map[string]any)
		id = d["id"].(string)
		revision = 1
		token = ""
		advance("claim", nil)
		advance("prepared", map[string]any{"candidate_id": "conditional-one", "candidate_url": "http://int.example.test"})
		action("accept", true, map[string]any{"evidence": checks, "authorization_id": authorization, "scope_review": "当前候选符合菜单修复范围", "user_comments_version": "stale"}, 409)
		action("accept", true, map[string]any{"evidence": checks, "authorization_id": authorization, "scope_review": "当前候选符合菜单修复范围", "user_comments_version": version()}, 200)
		advance("accepted", nil)
		var phase string
		f.QueryRow(t, `SELECT phase FROM issue_delivery WHERE id=$1`, id).Scan(&phase)
		if phase != "approved" {
			t.Fatalf("未自动接续发布：%s", phase)
		}
		fresh := f.Insert(t, "comment", fxt.Cols{"issue_id": issue, "workspace_id": f.WorkspaceID, "author_type": "member", "author_id": f.UserID, "content": "先别发布了"})
		c, v = call(h.AdvanceDelivery, false, map[string]any{"id": id, "revision": revision, "worker_token": token, "action": "claim", "source_commit": body["source_commit"]})
		want(c, 409, v)
		f.QueryRow(t, `SELECT phase,revision FROM issue_delivery WHERE id=$1`, id).Scan(&phase, &revision)
		if phase != "awaiting_review" {
			t.Fatal("新要求未阻断发布")
		}
		var notices int
		f.QueryRow(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND content LIKE '%等待授权范围复核%'`, issue).Scan(&notices)
		if notices != 1 {
			t.Fatalf("授权核对通知数量：%d", notices)
		}
		c, v = call(h.AdvanceDelivery, false, map[string]any{"id": id, "revision": revision, "action": "notify-authorization"})
		want(c, 200, v)
		f.QueryRow(t, `SELECT count(*) FROM comment WHERE issue_id=$1 AND content LIKE '%等待授权范围复核%'`, issue).Scan(&notices)
		if notices != 1 {
			t.Fatal("通知重试产生重复评论")
		}
		c, v = call(h.RevokeIssueReleaseAuthorization, true, map[string]any{"plan_id": planID, "revision": 1, "comment_id": fresh, "comment_revision": 1, "scope": "用户要求暂停发布"}, "id", issue)
		want(c, 200, v)
		action("approve", true, map[string]any{"authorization_id": authorization, "scope_review": "复用旧批准", "user_comments_version": version()}, 409)
		f.Exec(t, `UPDATE comment SET revision=2,content='只做联调',updated_at=clock_timestamp() WHERE id=$1`, grant)
		c, v = call(h.ConfirmIssuePlan, true, confirmationBody, "id", issue)
		want(c, 409, v)
	})

	t.Run("button-approval-current-membership", func(t *testing.T) {
		issue = f.Issue(t, "页面按钮批准")
		pc := f.Insert(t, "comment", fxt.Cols{"issue_id": issue, "workspace_id": f.WorkspaceID, "author_type": "member", "author_id": f.UserID, "content": "页面批准方案"})
		c, v := call(h.RegisterIssuePlan, false, map[string]any{"comment_id": pc, "revision": 1}, "id", issue)
		want(c, 200, v)
		c, v = call(h.ConfirmIssuePlan, false, map[string]any{"plan_id": v["plan"].(map[string]any)["id"], "revision": 1}, "id", issue)
		want(c, 200, v)
		body["operation_id"] = dbid.NewV7().String()
		c, v = call(h.SubmitIssueDelivery, true, body, "id", issue)
		want(c, 200, v)
		id = v["deliveries"].([]any)[0].(map[string]any)["id"].(string)
		revision = 1
		token = ""
		advance("claim", nil)
		advance("prepared", map[string]any{"candidate_id": "button-one", "candidate_url": "http://int.example.test"})
		action("accept", true, map[string]any{"evidence": checks}, 200)
		advance("accepted", nil)
		action("approve", false, nil, 200)
		c, v = call(h.CheckDeliveryAuthorization, false, nil, "deliveryId", id)
		want(c, 200, v)
		if v["ready"] != true {
			t.Fatal("真实按钮授权未生效")
		}
		replacement := f.User(t, "成员变更样本", schema+"-replacement@example.test", fxt.Cols{"account": schema + "-replacement"})

		f.Member(t, f.WorkspaceID, replacement, "admin")
		original := f.UserID
		f.Exec(t, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, f.WorkspaceID, original)
		f.UserID = replacement
		c, v = call(h.CheckDeliveryAuthorization, false, nil, "deliveryId", id)
		f.UserID = original
		f.Member(t, f.WorkspaceID, original, "owner")
		want(c, 200, v)
		if v["ready"] != false {
			t.Fatal("批准者已离开工作区仍能发布")
		}
	})

	t.Log("PASS actual PostgreSQL and handlers: versioned plan, human-only approval, idempotent submit, int evidence gate, production confirmation, persisted phases; no model or production deployment")
}
