package supervision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/service"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func TestIntPersistenceAndResume(t *testing.T) {
	if os.Getenv("MULTICA_SUPERVISION_INT_TEST") != "1" {
		t.Skip("requires explicit fixed-int database validation")
	}
	ctx := context.Background()
	raw, err := os.ReadFile("/data/ida/multica/.run/env/int-services.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]map[string]string
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["server"]["MULTICA_ENV_ID"] != "int" {
		t.Fatal("not integration environment")
	}
	pool, err := pgxpool.New(ctx, cfg["server"]["DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := dbfx.New(pool, "", "")
	stamp := fmt.Sprint(time.Now().UnixNano())
	f.UserID = f.User(t, "监督持久化验收", "supervision-"+stamp+"@example.test", dbfx.Cols{"account": "supervision-" + stamp})
	f.WorkspaceID = f.Workspace(t, "监督持久化验收", "supervision-"+stamp)
	f.Member(t, f.WorkspaceID, f.UserID, "owner")
	f.InsertNoID(t, "supervision_config", dbfx.Cols{"workspace_id": f.WorkspaceID, "owner_id": f.UserID, "enabled": true}, "workspace_id=$1", f.WorkspaceID)
	issues := make([]string, 100)
	for i := range issues {
		issues[i] = f.Issue(t, fmt.Sprintf("监督持久化验收 %03d", i))
	}
	svc := New(pool, nil)
	if err = svc.inventory(ctx, f.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if n := f.Count(t, "SELECT count(*) FROM supervision_subject WHERE workspace_id=$1", f.WorkspaceID); n != 100 {
		t.Fatalf("inventory=%d", n)
	}
	// 将其余任务设为已登记，后续竞态与调度验证只操作目标任务。
	f.Exec(t, "UPDATE supervision_subject SET reviewed_version=desired_version WHERE workspace_id=$1", f.WorkspaceID)
	if n := f.Count(t, "SELECT count(*) FROM supervision_subject WHERE workspace_id=$1 AND reviewed_version=desired_version AND last_checked_at IS NULL", f.WorkspaceID); n != 100 {
		t.Fatalf("no-run accounting=%d", n)
	}
	runtime := f.Runtime(t, "离线夹具", dbfx.Cols{"status": "offline"})
	agent := f.Agent(t, "夹具", runtime)
	sourceTask := f.Task(t, agent, dbfx.Cols{"runtime_id": runtime, "issue_id": issues[0], "status": "completed", "completed_at": dbfx.Raw("now()")})
	note := f.Comment(t, issues[0], "/note 请监督者核验映射，不要直接派发。")
	large := f.Insert(t, "task_message", dbfx.Cols{"task_id": sourceTask, "seq": 20, "type": "text", "content": strings.Repeat("x", 10*1024*1024) + "END-OF-EVIDENCE"})
	late := f.Insert(t, "task_message", dbfx.Cols{"task_id": sourceTask, "seq": 19, "type": "text", "content": "迟到的较小序号"})
	_ = late
	review := f.Task(t, agent, dbfx.Cols{"runtime_id": runtime, "status": "completed", "completed_at": dbfx.Raw("now()"), "context": dbfx.Raw(`'{"type":"quality_supervision"}'::jsonb`)})
	t.Cleanup(func() {
		for _, table := range []string{"supervision_action", "supervision_finding", "supervision_batch", "supervision_material", "supervision_subject", "supervision_config"} {
			if _, e := pool.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1", f.WorkspaceID); e != nil {
				t.Errorf("cleanup %s: %v", table, e)
			}
		}
	})
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chunks, readErr := svc.materials(ctx, tx, f.WorkspaceID, issues[0])
	_ = tx.Rollback(ctx)
	if readErr != nil {
		t.Fatal(readErr)
	}
	messageOrder := []string{}
	for _, m := range chunks {
		if m.Kind == "message" {
			messageOrder = append(messageOrder, m.ID)
		}
	}
	if len(messageOrder) != 2 || messageOrder[0] != late || messageOrder[1] != large {
		t.Fatalf("messages did not follow source sequence: %v", messageOrder)
	}
	noteFound := false
	for _, m := range chunks {
		if len([]rune(m.Text)) > 8000 {
			t.Fatal("unbounded material body")
		}
		if m.ID == note {
			noteFound = m.Note
		}
	}
	if !noteFound {
		t.Fatal("supervisor note not marked in real batch materials")
	}
	var target, generation int64
	f.QueryRow(t, "SELECT desired_version FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$2", f.WorkspaceID, issues[0]).Scan(&target)
	f.QueryRow(t, "SELECT generation FROM supervision_material WHERE workspace_id=$1 AND kind='comment' AND source_id=$2", f.WorkspaceID, note).Scan(&generation)
	page, err := svc.ReadEvidence(ctx, f.WorkspaceID, "comment", note, 0)
	if err != nil {
		t.Fatal(err)
	}
	material := Material{Evidence: Evidence{Kind: "comment", ID: note}, Generation: generation, Offset: 0, End: page.End, Length: page.Length, Text: page.Text}
	refs, _ := json.Marshal([]Material{material})
	batch := f.Insert(t, "supervision_batch", dbfx.Cols{"workspace_id": f.WorkspaceID, "issue_id": issues[0], "target_version": target, "task_id": review, "status": "running", "materials": refs})
	f.Exec(t, "UPDATE comment SET content=content||' 新要求：按用户最新决定核验。' WHERE id=$1", note)
	savedPage, err := svc.ReadBatchEvidence(ctx, f.WorkspaceID, batch, "comment", note)
	if err != nil || !savedPage.Snapshot || savedPage.Text != page.Text || strings.Contains(savedPage.Text, "新要求") {
		t.Fatalf("edited source replaced the review snapshot: %+v %v", savedPage, err)
	}
	if _, err = svc.ReadBatchEvidence(ctx, f.UserID, batch, "comment", note); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("snapshot escaped workspace scope: %v", err)
	}
	result := Result{Summary: "旧代次已读，仍有新要求待核验", Efficiency: "clear", Correctness: "uncertain", Gaps: []string{"待核验新要求"}, Covered: []Evidence{material.Evidence}, Findings: []Finding{}, Resolutions: []Resolution{}}
	if err = svc.Commit(ctx, f.WorkspaceID, batch, review, result); err != nil {
		t.Fatal(err)
	}
	if n := f.Count(t, "SELECT count(*) FROM supervision_material WHERE workspace_id=$1 AND source_id=$2 AND generation>reviewed_generation AND reviewed_offset=0", f.WorkspaceID, note); n != 1 {
		t.Fatal("stale callback consumed newer edit")
	}
	result.Summary = "重复回调不得覆盖"
	if err = svc.Commit(ctx, f.WorkspaceID, batch, review, result); err != nil {
		t.Fatal(err)
	}
	var saved string
	f.QueryRow(t, "SELECT summary FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$2", f.WorkspaceID, issues[0]).Scan(&saved)
	if saved == result.Summary {
		t.Fatal("duplicate callback overwrote result")
	}

	// 同一问题复查复用原编号，即使模型调整类别名称，也只保留一条待处理记录。
	f.QueryRow(t, "SELECT generation FROM supervision_material WHERE workspace_id=$1 AND source_id=$2 AND kind='comment'", f.WorkspaceID, note).Scan(&material.Generation)
	page, err = svc.ReadEvidence(ctx, f.WorkspaceID, "comment", note, 0)
	if err != nil {
		t.Fatal(err)
	}
	material.End = page.End
	material.Length = page.Length
	material.Text = page.Text
	refs, _ = json.Marshal([]Material{material})
	makeBatch := func() string {
		return f.Insert(t, "supervision_batch", dbfx.Cols{"workspace_id": f.WorkspaceID, "issue_id": issues[0], "target_version": target, "task_id": review, "status": "running", "materials": refs})
	}
	finding := Finding{Code: "mapping", Title: "映射待核验", Description: "依据本轮用户备注核验", Impact: "字段取值可能不符", Recommendation: "检查实际字段映射", Confidence: "risk", Evidence: []Evidence{material.Evidence}}
	result.Findings = []Finding{finding}
	result.Summary = "首次记录风险"
	findingBatch := makeBatch()
	if err = svc.Commit(ctx, f.WorkspaceID, findingBatch, review, result); err != nil {
		t.Fatal(err)
	}
	var savedEvidence []byte
	f.QueryRow(t, "SELECT evidence FROM supervision_finding WHERE workspace_id=$1", f.WorkspaceID).Scan(&savedEvidence)
	var findingReferences []evidenceSnapshot
	if err = json.Unmarshal(savedEvidence, &findingReferences); err != nil || len(findingReferences) != 1 || findingReferences[0].BatchID != findingBatch {
		t.Fatalf("finding lost its immutable snapshot binding: %s %v", savedEvidence, err)
	}
	f.QueryRow(t, "SELECT id::text FROM supervision_finding WHERE workspace_id=$1", f.WorkspaceID).Scan(&finding.ID)
	f.Insert(t, "supervision_action", dbfx.Cols{"workspace_id": f.WorkspaceID, "finding_id": finding.ID, "actor_id": f.UserID, "operation_id": util.UUIDToString(dbid.NewV7()), "action": "ignored", "reason": "用户确认本轮只检查接口调用计划，不要求发起网络请求"})
	f.Exec(t, "UPDATE supervision_finding SET status='ignored' WHERE id=$1", finding.ID)
	var decisionSnapshot []byte
	f.QueryRow(t, currentIssueSnapshotSQL, issues[0], f.WorkspaceID).Scan(&decisionSnapshot)
	var decisions struct {
		Findings []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"finding_decisions"`
	}
	if err = json.Unmarshal(decisionSnapshot, &decisions); err != nil {
		t.Fatal(err)
	}
	if len(decisions.Findings) != 1 || decisions.Findings[0].ID != finding.ID || decisions.Findings[0].Status != "ignored" || !strings.Contains(decisions.Findings[0].Reason, "不要求发起网络请求") {
		t.Fatalf("manual disposition lost before next review: %+v", decisions)
	}
	f.Exec(t, "UPDATE supervision_finding SET status='open' WHERE id=$1", finding.ID)
	finding.Code = "mapping_category_renamed"
	result.Findings = []Finding{finding}
	if err = svc.Commit(ctx, f.WorkspaceID, makeBatch(), review, result); err != nil {
		t.Fatal(err)
	}
	if n := f.Count(t, "SELECT count(*) FROM supervision_finding WHERE workspace_id=$1", f.WorkspaceID); n != 1 {
		t.Fatalf("duplicate finding after recheck: %d", n)
	}
	result.Findings = []Finding{}
	result.Resolutions = []Resolution{{ID: finding.ID, Reason: "新证据已核对", Evidence: []Evidence{material.Evidence}}}
	if err = svc.Commit(ctx, f.WorkspaceID, makeBatch(), review, result); err != nil {
		t.Fatal(err)
	}
	if n := f.Count(t, "SELECT count(*) FROM supervision_action WHERE workspace_id=$1 AND action='auto_resolved' AND reason='新证据已核对'", f.WorkspaceID); n != 1 {
		t.Fatal("resolution audit missing")
	}
	if n := f.Count(t, "SELECT count(*) FROM supervision_finding WHERE workspace_id=$1 AND status='resolved'", f.WorkspaceID); n != 1 {
		t.Fatal("resolution did not close finding")
	}
	f.QueryRow(t, currentIssueSnapshotSQL, issues[0], f.WorkspaceID).Scan(&decisionSnapshot)
	if err = json.Unmarshal(decisionSnapshot, &decisions); err != nil || len(decisions.Findings) != 1 || decisions.Findings[0].ID != finding.ID || decisions.Findings[0].Status != "resolved" || decisions.Findings[0].Reason != "新证据已核对" {
		t.Fatalf("resolved finding identity lost before regression review: %+v %v", decisions, err)
	}

	// 新进程对象从数据库恢复分片位置，不依赖上一轮内存。
	restarted := New(pool, nil)
	first, err := restarted.ReadEvidence(ctx, f.WorkspaceID, "message", large, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Text) > 8000 || first.End >= first.Length || first.Length < 10*1024*1024 {
		t.Fatalf("invalid bounded page: end=%d length=%d", first.End, first.Length)
	}
	f.Exec(t, "UPDATE supervision_material SET reviewed_offset=$3 WHERE workspace_id=$1 AND source_id=$2", f.WorkspaceID, large, first.End)
	afterLarge := f.Insert(t, "task_message", dbfx.Cols{"task_id": sourceTask, "seq": 21, "type": "text", "content": "必须等待前一条全文审查"})
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resumed, readErr := restarted.materials(ctx, tx, f.WorkspaceID, issues[0])
	_ = tx.Rollback(ctx)
	if readErr != nil {
		t.Fatal(readErr)
	}
	foundResume := false
	for _, m := range resumed {
		if m.ID == afterLarge {
			t.Fatal("later message overtook unread source tail")
		}
		if m.ID != large {
			continue
		}
		foundResume = true
		if m.UnreadOffset != first.End || m.Offset != first.End-1000 || m.End <= first.End {
			t.Fatalf("resume skipped overlap or failed to advance: %+v", m.Evidence)
		}
		if m.Text[:1000] != first.Text[len(first.Text)-1000:] {
			t.Fatal("overlap differs from original text")
		}
	}
	if !foundResume {
		t.Fatal("resumed source missing")
	}
	next, err := restarted.ReadEvidence(ctx, f.WorkspaceID, "message", large, first.End)
	if err != nil {
		t.Fatal(err)
	}
	if next.Offset != first.End || next.End <= next.Offset {
		t.Fatal("resume lost offset")
	}
	tail, err := restarted.ReadEvidence(ctx, f.WorkspaceID, "message", large, first.Length-100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tail.Text, "END-OF-EVIDENCE") {
		t.Fatal("long evidence tail lost")
	}
	if n := f.Count(t, "SELECT count(*) FROM supervision_material WHERE workspace_id=$1 AND kind='message' AND run_id=$2", f.WorkspaceID, sourceTask); n != 3 {
		t.Fatalf("late-message coverage=%d", n)
	}
	// 旧消息已审查后插入更早的消息，后继原文仍进入这一轮上下文。
	f.Exec(t, "UPDATE supervision_material SET reviewed_generation=generation WHERE workspace_id=$1 AND source_id=$2", f.WorkspaceID, late)
	lateEarlier := f.Insert(t, "task_message", dbfx.Cols{"task_id": sourceTask, "seq": 18, "type": "text", "content": "更迟到的工具请求"})
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	withLate, readErr := restarted.materials(ctx, tx, f.WorkspaceID, issues[0])
	_ = tx.Rollback(ctx)
	if readErr != nil {
		t.Fatal(readErr)
	}
	seenEarlier, seenNeighbor := false, false
	for _, m := range withLate {
		if m.ID == lateEarlier && !m.ContextOnly {
			seenEarlier = true
		}
		if m.ID == late && m.ContextOnly && strings.Contains(m.Text, "迟到的较小序号") {
			seenNeighbor = true
		}
	}
	if !seenEarlier || !seenNeighbor {
		t.Fatal("late insertion lost original neighboring text")
	}
	// 运行ID绑定验证独立于 JSON 结构校验。
	if err = svc.Commit(ctx, f.WorkspaceID, batch, util.UUIDToString(dbid.NewV7()), result); err == nil {
		t.Fatal("foreign task accepted")
	}

	// 持续新增日志不能让运行中任务无限等待安静窗口。
	f.Task(t, agent, dbfx.Cols{"runtime_id": runtime, "issue_id": issues[0], "status": "running", "started_at": dbfx.Raw("now()")})
	f.Exec(t, "UPDATE supervision_config SET agent_id=$2,interval_seconds=60 WHERE workspace_id=$1", f.WorkspaceID, agent)
	f.Exec(t, "UPDATE supervision_subject SET last_checked_at=now()-interval '10 minutes',changed_at=now(),desired_version=reviewed_version+1,force_review=false WHERE workspace_id=$1 AND issue_id=$2", f.WorkspaceID, issues[0])
	svc.Tasks = service.NewTaskService(db.New(pool), pool, nil, nil)
	svc.Authorize = func(context.Context, string, string, string) bool { return true }
	// 不支持的夹具 provider 在选中待审任务后拒绝派发，不会启动用户 CLI。
	if err = svc.dispatch(ctx, f.WorkspaceID); err == nil || !strings.Contains(err.Error(), "首版监督支持") {
		t.Fatalf("continuous updates starved review: %v", err)
	}
	// 用户点选的重新审查先于首轮队列中的其他任务。
	f.Exec(t, "UPDATE supervision_subject SET reviewed_version=0,last_checked_at=NULL WHERE workspace_id=$1 AND issue_id=$2", f.WorkspaceID, issues[1])
	f.Exec(t, "UPDATE supervision_material SET reviewed_generation=generation WHERE workspace_id=$1 AND issue_id=$2", f.WorkspaceID, issues[1])
	f.Exec(t, "UPDATE supervision_subject SET force_review=true WHERE workspace_id=$1 AND issue_id=$2", f.WorkspaceID, issues[0])
	if err = svc.dispatch(ctx, f.WorkspaceID); err == nil || !strings.Contains(err.Error(), "首版监督支持") {
		t.Fatalf("manual recheck lost priority: %v", err)
	}

	// 创建时间较早的评论被编辑后，必须进入最近决定清单，而非被六条新评论挤掉。
	old := f.Comment(t, issues[99], "最初允许发布")
	f.Exec(t, "UPDATE comment SET created_at=now()-interval '1 day',updated_at=now()-interval '1 day' WHERE id=$1", old)
	for i := 0; i < 7; i++ {
		f.Comment(t, issues[99], fmt.Sprintf("后续普通评论%d", i))
	}
	f.Exec(t, "UPDATE comment SET content='最新决定：生产前须确认',updated_at=now()+interval '1 second' WHERE id=$1", old)
	var current struct {
		PreviousAttemptError string `json:"previous_attempt_error"`
		Comments             []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"latest_user_comments"`
	}
	var snapshot []byte
	f.Exec(t, "UPDATE supervision_subject SET error='覆盖引用未知或重复' WHERE workspace_id=$1 AND issue_id=$2", f.WorkspaceID, issues[99])
	f.QueryRow(t, currentIssueSnapshotSQL, issues[99], f.WorkspaceID).Scan(&snapshot)
	if err := json.Unmarshal(snapshot, &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Comments) != 6 || current.Comments[0].ID != old || current.Comments[0].Content != "最新决定：生产前须确认" {
		t.Fatalf("edited decision omitted: %+v", current)
	}
	if current.PreviousAttemptError != "覆盖引用未知或重复" {
		t.Fatal("retry lost the previous validation failure")
	}
	t.Log("PASS: 100-task inventory; note generation race; duplicate callbacks; independent restart; 10MB bounded reads and tail; late seq; task fence; edited user decision")
}
