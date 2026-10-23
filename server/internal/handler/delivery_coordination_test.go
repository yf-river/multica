package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type coordinationOwner struct {
	Token      string `json:"token"`
	Generation int64  `json:"generation"`
}

func coordinationFixture(t *testing.T) (string, string) {
	t.Helper()
	issue := dbfx.Issue(t, "交付并发验收")
	comment := dbfx.Insert(t, "comment", testutil.Cols{"workspace_id": testWorkspaceID, "issue_id": issue, "author_type": "member", "author_id": testUserID, "content": "已确认的交付计划"})
	plan := dbfx.Insert(t, "issue_delivery_plan", testutil.Cols{"id": dbid.NewV7(), "workspace_id": testWorkspaceID, "issue_id": issue, "comment_id": comment, "comment_revision": 1, "confirmation_id": dbid.NewV7(), "confirmed_by": testUserID, "confirmed_at": testutil.Raw("now()")})
	dbfx.Cleanup(t, `DELETE FROM delivery_owner WHERE issue_id=$1`, issue)
	dbfx.Cleanup(t, `DELETE FROM delivery_evidence WHERE issue_id=$1`, issue)
	dbfx.Cleanup(t, `DELETE FROM issue_delivery WHERE issue_id=$1`, issue)
	return issue, plan
}
func ownerRequest(issue, action, execution string, o coordinationOwner, evidence string) *http.Request {
	return withURLParam(newRequest("POST", "/api/issues/"+issue+"/delivery-owner", map[string]any{"action": action, "execution_id": execution, "token": o.Token, "generation": o.Generation, "recovery_evidence": evidence}), "id", issue)
}
func deliveryRequest(issue string, body any, o coordinationOwner) *http.Request {
	r := withURLParam(newRequest("POST", "/api/issues/"+issue+"/deliveries", body), "id", issue)
	r.Header.Set("X-Delivery-Token", o.Token)
	r.Header.Set("X-Delivery-Generation", strconv.FormatInt(o.Generation, 10))
	return r
}
func submitBody(commit string) map[string]any {
	return map[string]any{"required_checks": businessChecks(), "operation_id": dbid.NewV7(), "source_commit": commit, "source_branch": "feature/coordination", "base_commit": strings.Repeat("b", 40), "evidence": []deliveryCheck{{Name: "本地检查", Status: "passed", Evidence: "实际通过"}}}
}

func TestDeliveryCoordinationConcurrentOwnersAndDuplicateSubmissions(t *testing.T) {
	issue, _ := coordinationFixture(t)
	var wg sync.WaitGroup
	owners := make([]coordinationOwner, 2)
	codes := make([]int, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r := testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "claim", "session-"+strconv.Itoa(n), coordinationOwner{}, ""))
			codes[n] = r.Code
			if r.Code == 200 {
				r.JSON(&owners[n])
			}
		}(n)
	}
	wg.Wait()
	winner := -1
	for n, c := range codes {
		if c == 200 {
			if winner != -1 {
				t.Fatal("两个会话同时领取成功")
			}
			winner = n
		} else if c != 409 {
			t.Fatalf("意外响应 %d", c)
		}
	}
	if winner < 0 {
		t.Fatal("没有成功的负责人")
	}
	o := owners[winner]
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			testutil.Call(t, testHandler.SubmitIssueDelivery, deliveryRequest(issue, submitBody(strings.Repeat("a", 40)), o)).Want(200)
		}()
	}
	wg.Wait()
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM issue_delivery WHERE issue_id=$1`, issue).Scan(&count)
	if count != 1 {
		t.Fatalf("相同版本产生 %d 份交付", count)
	}
	testutil.Call(t, testHandler.SubmitIssueDelivery, deliveryRequest(issue, submitBody(strings.Repeat("c", 40)), o)).Want(409)
	dbfx.QueryRow(t, `SELECT count(*) FROM issue_delivery WHERE issue_id=$1 AND phase='waiting_int'`, issue).Scan(&count)
	if count != 1 {
		t.Fatal("新提交破坏了原交付")
	}
}
func TestDeliveryCoordinationHandoffFencesOldSession(t *testing.T) {
	issue, _ := coordinationFixture(t)
	var first, second coordinationOwner
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "claim", "old-session", first, "")).Want(200).JSON(&first)
	dbfx.Exec(t, `UPDATE delivery_owner SET expires_at=now()-interval '1 second' WHERE issue_id=$1`, issue)
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "claim", "new-session", second, "")).Want(409)
	testutil.Call(t, testHandler.ManageDeliveryOwner, ownerRequest(issue, "recover", "new-session", second, "旧进程已退出、候选未变、环境身份已核对")).Want(200).JSON(&second)
	if second.Generation <= first.Generation {
		t.Fatal("交接未增加代次")
	}
	testutil.Call(t, testHandler.SubmitIssueDelivery, deliveryRequest(issue, submitBody(strings.Repeat("a", 40)), first)).Want(409)
	testutil.Call(t, testHandler.SubmitIssueDelivery, deliveryRequest(issue, submitBody(strings.Repeat("a", 40)), second)).Want(200)
	var public map[string]any
	testutil.Call(t, testHandler.ManageDeliveryOwner, withURLParam(newRequest("GET", "/owner", nil), "id", issue)).Want(200).JSON(&public)
	if public["token"] != nil {
		t.Fatal("查询暴露负责人凭据")
	}
}
func coordinationDelivery(t *testing.T, issue, plan, phase string) (string, string) {
	t.Helper()
	token := dbid.NewV7().String()
	d := dbfx.Insert(t, "issue_delivery", testutil.Cols{"id": dbid.NewV7(), "workspace_id": testWorkspaceID, "issue_id": issue, "operation_id": dbid.NewV7(), "plan_id": plan, "source_commit": strings.Repeat("a", 40), "source_branch": "feature/test", "base_commit": strings.Repeat("b", 40), "phase": phase, "verification_comment_id": dbid.NewV7(), "release_comment_id": dbid.NewV7(), "worker_token": token, "plan_snapshot": testutil.Raw("'{}'::jsonb")})
	return d, token
}
func TestDeliveryCoordinationABCPublicationAndStaleRelease(t *testing.T) {
	issue, plan := coordinationFixture(t)
	a, at := coordinationDelivery(t, issue, plan, "preparing_release")
	b, bt := coordinationDelivery(t, issue, plan, "preparing_release")
	c, ct := coordinationDelivery(t, issue, plan, "preparing_release")
	dbfx.Cleanup(t, `DELETE FROM delivery_publication WHERE resource='int-prod'`)
	call := func(action, id, token string, g int64, evidence string) *testutil.Response {
		return testutil.Call(t, testHandler.PublicationLease, newRequest("POST", "/api/delivery-queue/publication", map[string]any{"action": action, "delivery_id": id, "worker_token": token, "generation": g, "recovery_evidence": evidence}))
	}
	var al, bl coordinationOwner
	call("claim", a, at, 0, "").Want(200).JSON(&al)
	call("claim", b, bt, 0, "").Want(409)
	call("claim", c, ct, 0, "").Want(409)
	call("release", a, at, al.Generation, "").Want(200)
	call("claim", b, bt, 0, "").Want(200).JSON(&bl)
	call("release", a, at, al.Generation, "").Want(409)
	dbfx.Exec(t, `UPDATE delivery_publication SET expires_at=now()-interval '1 second' WHERE resource='int-prod'`)
	call("heartbeat", b, bt, bl.Generation, "").Want(409)
	call("claim", c, ct, 0, "").Want(409)
	var resumed coordinationOwner
	call("resume", b, bt, bl.Generation, "已独占进程锁并核对环境恢复状态").Want(200).JSON(&resumed)
	call("release", b, bt, bl.Generation, "").Want(409)
	call("release", b, bt, resumed.Generation, "").Want(200)
	call("claim", c, ct, 0, "").Want(200)
}
func TestDeliveryCoordinationEvidenceSurvivesCandidateChanges(t *testing.T) {
	issue, plan := coordinationFixture(t)
	d, token := coordinationDelivery(t, issue, plan, "testing")
	dbfx.Exec(t, `UPDATE issue_delivery SET plan_snapshot='{"required_checks":[{"name":"真实功能","stage":"verification"},{"name":"组合业务","stage":"integration"}]}'::jsonb WHERE id=$1`, d)
	identity := `{"runtime_inputs":"code","schema":"schema","suite":"suite","configuration":"config"}`
	dbfx.Insert(t, "delivery_evidence", testutil.Cols{"id": dbid.NewV7(), "workspace_id": testWorkspaceID, "issue_id": issue, "delivery_id": d, "candidate_id": "original", "source_commit": strings.Repeat("a", 40), "identity": testutil.Raw("'" + identity + "'::jsonb"), "evidence": testutil.Raw(`'[{"name":"真实功能","status":"passed","evidence":"已实际执行"}]'::jsonb`)})
	prepare := func(candidate string, raw json.RawMessage, rev int64) {
		testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/api/delivery-queue/advance", map[string]any{"id": d, "revision": rev, "worker_token": token, "action": "prepared", "candidate_id": candidate, "candidate_url": "http://int.example", "candidate_identity": raw})).Want(200)
	}
	prepare("same-inputs", json.RawMessage(identity), 1)
	var passed bool
	dbfx.QueryRow(t, `SELECT acceptance_submitted FROM issue_delivery WHERE id=$1`, d).Scan(&passed)
	if !passed {
		t.Fatal("相同输入未复用验收")
	}
	prepare("changed-inputs", json.RawMessage(`{"runtime_inputs":"changed"}`), 2)
	dbfx.QueryRow(t, `SELECT acceptance_submitted FROM issue_delivery WHERE id=$1`, d).Scan(&passed)
	if passed {
		t.Fatal("输入变化仍被判通过")
	}
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM delivery_evidence WHERE delivery_id=$1`, d).Scan(&count)
	if count != 2 {
		t.Fatalf("历史证据丢失: %d", count)
	}
}
