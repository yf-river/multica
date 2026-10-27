package supervision

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/service"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// roundFixture 建一套最小的监督工作区：配置、运行时、监督 Agent 和一条任务。
type roundFixture struct {
	pool      *pgxpool.Pool
	svc       *Service
	f         *dbfx.Fixture
	workspace string
	runtime   string
	agent     string
}

func newRoundFixture(t *testing.T) *roundFixture {
	t.Helper()
	ctx := context.Background()
	url, targetErr := dbfx.TestDatabaseURL()
	if targetErr != nil {
		t.Fatal(targetErr)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("skipping round tests: %v", err)
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("skipping round tests: database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)
	f := dbfx.New(pool, "", "")
	stamp := fmt.Sprint(time.Now().UnixNano())
	f.UserID = f.User(t, "轮次验收", "round-"+stamp+"@example.test", dbfx.Cols{"account": "round-" + stamp})
	f.WorkspaceID = f.Workspace(t, "轮次验收", "round-"+stamp)
	f.Member(t, f.WorkspaceID, f.UserID, "owner")
	runtime := f.Runtime(t, "轮次夹具", dbfx.Cols{"provider": "codebuddy", "status": "online", "daemon_id": "no-daemon-" + stamp})
	agent := f.Agent(t, "轮次监督夹具", runtime, dbfx.Cols{"max_concurrent_tasks": 3})
	f.InsertNoID(t, "supervision_config", dbfx.Cols{"workspace_id": f.WorkspaceID, "agent_id": agent, "owner_id": f.UserID, "enabled": true}, "workspace_id=$1", f.WorkspaceID)
	svc := New(pool, service.NewTaskService(db.New(pool), pool, nil, nil))
	svc.Authorize = func(context.Context, string, string, string) bool { return true }
	t.Cleanup(func() {
		for _, table := range []string{"supervision_frozen_material", "supervision_investigation", "supervision_action", "supervision_finding", "supervision_batch_round", "supervision_batch", "supervision_material", "supervision_round", "supervision_subject", "supervision_config"} {
			if _, e := pool.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1", f.WorkspaceID); e != nil {
				t.Errorf("cleanup %s: %v", table, e)
			}
		}
	})
	return &roundFixture{pool: pool, svc: svc, f: f, workspace: f.WorkspaceID, runtime: runtime, agent: agent}
}

func (x *roundFixture) issue(t *testing.T, status string) string {
	t.Helper()
	return x.f.Issue(t, "轮次任务", dbfx.Cols{"status": status})
}

func (x *roundFixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	return x.f.Count(t, sql, args...)
}

// batchResult 用批次实际材料构造一份可提交的结论，覆盖清单必须与材料一一对应。
func (x *roundFixture) batchResult(t *testing.T, batch string) Result {
	t.Helper()
	var raw []byte
	if err := x.pool.QueryRow(context.Background(), `SELECT materials FROM supervision_batch WHERE id=$1`, batch).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var materials []Material
	if err := json.Unmarshal(raw, &materials); err != nil {
		t.Fatal(err)
	}
	covered := []Evidence{}
	for _, m := range materials {
		if !m.ContextOnly {
			covered = append(covered, m.Evidence)
		}
	}
	return Result{Summary: "本轮材料已核对", Efficiency: "clear", Correctness: "clear", Gaps: []string{}, Findings: []Finding{}, Resolutions: []Resolution{}, Covered: covered}
}

func (x *roundFixture) dispatchOnce(t *testing.T) {
	t.Helper()
	if err := x.svc.dispatch(context.Background(), x.workspace); err != nil {
		t.Fatal(err)
	}
}

// TestRoundOnlyStartsAfterCompletion 验证自动监督只在任务完成并建立轮次后派发，
// 重复扫描与并发补齐不会重复建轮。
func TestRoundOnlyStartsAfterCompletion(t *testing.T) {
	x := newRoundFixture(t)
	issue := x.issue(t, "in_progress")
	task := x.f.Task(t, x.agent, dbfx.Cols{"runtime_id": x.runtime, "issue_id": issue, "status": "completed"})
	x.f.Insert(t, "task_message", dbfx.Cols{"task_id": task, "seq": 1, "type": "text", "content": "开发过程中的执行记录"})

	if err := x.svc.inventory(context.Background(), x.workspace); err != nil {
		t.Fatal(err)
	}
	if err := x.svc.reconcileRounds(context.Background(), x.workspace); err != nil {
		t.Fatal(err)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_round WHERE workspace_id=$1", x.workspace); n != 0 {
		t.Fatalf("未完成任务建立了轮次：%d", n)
	}
	x.dispatchOnce(t)
	if n := x.count(t, "SELECT count(*) FROM supervision_batch WHERE workspace_id=$1", x.workspace); n != 0 {
		t.Fatalf("未完成任务自动派发：%d", n)
	}

	x.f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", issue)
	// 4. 并发扫描像多实例重启一样同时补齐，唯一索引只允许一个开放轮次。
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = x.svc.reconcileRounds(context.Background(), x.workspace) }()
	}
	wg.Wait()
	if n := x.count(t, "SELECT count(*) FROM supervision_round WHERE workspace_id=$1 AND issue_id=$2", x.workspace, issue); n != 1 {
		t.Fatalf("完成轮次重复创建：%d", n)
	}
	var cutoff time.Time
	if err := x.pool.QueryRow(context.Background(), `SELECT material_cutoff FROM supervision_round WHERE workspace_id=$1 AND issue_id=$2 AND status='open'`, x.workspace, issue).Scan(&cutoff); err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.OpenRound(context.Background(), x.workspace, issue, true); err != nil {
		t.Fatal(err)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_round WHERE workspace_id=$1 AND issue_id=$2", x.workspace, issue); n != 1 {
		t.Fatalf("重复完成请求新建了轮次：%d", n)
	}
	// 5. 完成时点的材料在范围内，自动派发的批次归属这一轮。
	x.dispatchOnce(t)
	if n := x.count(t, "SELECT count(*) FROM supervision_batch b JOIN supervision_batch_round br ON br.batch_id=b.id AND br.scope='completion' WHERE b.workspace_id=$1", x.workspace); n != 1 {
		t.Fatalf("完成后未按轮次派发：%d", n)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_material WHERE workspace_id=$1 AND changed_at>$2::timestamptz", x.workspace, cutoff); n != 0 {
		t.Fatalf("完成快照材料晚于截止时间：%d", n)
	}
}

// TestReopenArchivesLateResultAndNewCompletionResumes 验证重开后旧轮次停止，
// 运行期间的迟到结果只归档，再次完成后只补审新增材料。
func TestReopenArchivesLateResultAndNewCompletionResumes(t *testing.T) {
	ctx := context.Background()
	x := newRoundFixture(t)
	issue := x.issue(t, "in_progress")
	task := x.f.Task(t, x.agent, dbfx.Cols{"runtime_id": x.runtime, "issue_id": issue, "status": "completed"})
	first := x.f.Insert(t, "task_message", dbfx.Cols{"task_id": task, "seq": 1, "type": "text", "content": "第一版交付记录"})
	x.f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", issue)

	if err := x.svc.inventory(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	if err := x.svc.reconcileRounds(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	// 6. 第一次完成正常审查并落地覆盖，作为后续轮次复用的依据。
	x.dispatchOnce(t)
	firstBatch, firstTask := x.runningBatch(t)
	if err := x.svc.Commit(ctx, x.workspace, firstBatch, firstTask, x.batchResult(t, firstBatch)); err != nil {
		t.Fatal(err)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_material WHERE workspace_id=$1 AND source_id=$2 AND reviewed_generation>=generation", x.workspace, first); n != 1 {
		t.Fatal("第一次完成没有登记覆盖")
	}

	// 7. 重开后再次完成：只补审新增材料，旧材料复用已有覆盖不重读。
	late := x.f.Comment(t, issue, "重开后补充的交付说明")
	x.f.Exec(t, "UPDATE issue SET status='in_progress' WHERE id=$1", issue)
	if err := x.svc.CloseOpenRound(ctx, x.workspace, issue, RoundCloseReopened); err != nil {
		t.Fatal(err)
	}
	x.f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", issue)
	if err := x.svc.reconcileRounds(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_round WHERE workspace_id=$1 AND issue_id=$2", x.workspace, issue); n != 2 {
		t.Fatalf("再次完成没有新建轮次：%d", n)
	}
	x.dispatchOnce(t)
	secondBatch, secondTask, secondRound := x.runningBatchWithRound(t)
	if secondRound == "" {
		t.Fatal("新轮次批次没有归属")
	}
	seen := x.batchMaterialIDs(t, secondBatch)
	if !seen[late] {
		t.Fatal("新轮次没有补审重开后新增的材料")
	}
	if seen[first] {
		t.Fatal("新轮次重复读取了已覆盖的旧材料")
	}

	// 8. 运行期间再次重开：迟到结果只归档，不更新当前结论和阅读进度。
	before := x.subjectSummary(t, issue)
	if err := x.svc.CloseOpenRound(ctx, x.workspace, issue, RoundCloseReopened); err != nil {
		t.Fatal(err)
	}
	if err := x.svc.Commit(ctx, x.workspace, secondBatch, secondTask, x.batchResult(t, secondBatch)); err != nil {
		t.Fatal(err)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_batch WHERE id=$1 AND status='cancelled' AND result IS NOT NULL", secondBatch); n != 1 {
		t.Fatal("迟到结果没有归档到审查历史")
	}
	if got := x.subjectSummary(t, issue); got != before {
		t.Fatalf("迟到结果更新了当前结论：%q", got)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_material WHERE workspace_id=$1 AND source_id=$2 AND reviewed_generation>=generation", x.workspace, late); n != 0 {
		t.Fatal("迟到结果推进了阅读进度")
	}

	// 9. 归档轮次的材料没有覆盖证据，下一次完成仍会补审。
	x.f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", issue)
	if err := x.svc.reconcileRounds(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	x.dispatchOnce(t)
	thirdBatch, _, thirdRound := x.runningBatchWithRound(t)
	if thirdRound == secondRound {
		t.Fatal("新轮次复用了已关闭的旧轮次")
	}
	if !x.batchMaterialIDs(t, thirdBatch)[late] {
		t.Fatal("归档轮次的材料没有重新进入下一轮")
	}
}

// runningBatch 返回当前运行中的监督批次与任务号。
func (x *roundFixture) runningBatch(t *testing.T) (string, string) {
	t.Helper()
	batch, task, _ := x.runningBatchWithRound(t)
	return batch, task
}

// runningBatchWithRound 额外返回批次归属的轮次编号。
func (x *roundFixture) runningBatchWithRound(t *testing.T) (string, string, string) {
	t.Helper()
	var batch, task, round string
	if err := x.pool.QueryRow(context.Background(), `SELECT b.id::text,COALESCE(b.task_id::text,''),COALESCE(br.round_id::text,'') FROM supervision_batch b
      LEFT JOIN supervision_batch_round br ON br.batch_id=b.id AND br.workspace_id=b.workspace_id
      WHERE b.workspace_id=$1 AND b.status='running' ORDER BY b.created_at DESC LIMIT 1`, x.workspace).Scan(&batch, &task, &round); err != nil {
		t.Fatal(err)
	}
	return batch, task, round
}

func (x *roundFixture) batchMaterialIDs(t *testing.T, batch string) map[string]bool {
	t.Helper()
	var raw []byte
	if err := x.pool.QueryRow(context.Background(), `SELECT materials FROM supervision_batch WHERE id=$1`, batch).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var materials []Material
	if err := json.Unmarshal(raw, &materials); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range materials {
		seen[m.ID] = true
	}
	return seen
}

func (x *roundFixture) subjectSummary(t *testing.T, issue string) string {
	t.Helper()
	var summary string
	if err := x.pool.QueryRow(context.Background(), `SELECT summary FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$2`, x.workspace, issue).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	return summary
}

// TestManualReviewOnUnfinishedTaskDoesNotContinueTracking 验证手动监督可以检查未完成
// 任务，但完成后不会自动继续派发。
func TestManualReviewOnUnfinishedTaskDoesNotContinueTracking(t *testing.T) {
	ctx := context.Background()
	x := newRoundFixture(t)
	issue := x.issue(t, "in_progress")
	task := x.f.Task(t, x.agent, dbfx.Cols{"runtime_id": x.runtime, "issue_id": issue, "status": "running"})
	x.f.Insert(t, "task_message", dbfx.Cols{"task_id": task, "seq": 1, "type": "text", "content": "进行到一半的执行记录"})
	if err := x.svc.inventory(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	x.f.Exec(t, "UPDATE supervision_subject SET force_review=true WHERE workspace_id=$1 AND issue_id=$2", x.workspace, issue)
	x.dispatchOnce(t)
	var batch, reviewTask string
	if err := x.pool.QueryRow(ctx, `SELECT b.id::text,COALESCE(b.task_id::text,'') FROM supervision_batch b JOIN supervision_batch_round br ON br.batch_id=b.id
      WHERE b.workspace_id=$1 AND br.scope='manual' AND b.status='running'`, x.workspace).Scan(&batch, &reviewTask); err != nil {
		t.Fatal(err)
	}
	result := x.batchResult(t, batch)
	if err := x.svc.Commit(ctx, x.workspace, batch, reviewTask, result); err != nil {
		t.Fatal(err)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$2 AND last_checked_at IS NOT NULL", x.workspace, issue); n != 1 {
		t.Fatal("手动检查没有保存结论")
	}
	// 手动检查结束后任务仍在开发中，不会自动再派发。
	x.f.Insert(t, "task_message", dbfx.Cols{"task_id": task, "seq": 2, "type": "text", "content": "后续进展"})
	x.dispatchOnce(t)
	if n := x.count(t, "SELECT count(*) FROM supervision_batch WHERE workspace_id=$1 AND status='running'", x.workspace); n != 0 {
		t.Fatal("手动检查恢复了未完成任务的自动追踪")
	}
}

func TestManualReviewAfterCompletionUsesLiveMaterials(t *testing.T) {
	for _, associatedRound := range []bool{false, true} {
		t.Run(fmt.Sprintf("associated_round_%t", associatedRound), func(t *testing.T) {
			ctx := context.Background()
			x := newRoundFixture(t)
			issue := x.issue(t, "done")
			x.f.Task(t, x.agent, dbfx.Cols{"runtime_id": x.runtime, "issue_id": issue, "status": "completed"})
			if err := x.svc.inventory(ctx, x.workspace); err != nil {
				t.Fatal(err)
			}
			if err := x.svc.reconcileRounds(ctx, x.workspace); err != nil {
				t.Fatal(err)
			}
			var completionRound string
			if err := x.pool.QueryRow(ctx, `SELECT id::text FROM supervision_round WHERE workspace_id=$1 AND issue_id=$2 AND status='open'`, x.workspace, issue).Scan(&completionRound); err != nil {
				t.Fatal(err)
			}
			late := x.f.Comment(t, issue, "手动检查需要核对完成之后的新材料")
			if err := x.svc.inventory(ctx, x.workspace); err != nil {
				t.Fatal(err)
			}
			x.f.Exec(t, `UPDATE supervision_subject SET force_review=true WHERE workspace_id=$1 AND issue_id=$2`, x.workspace, issue)
			x.dispatchOnce(t)
			batch, task, round := x.runningBatchWithRound(t)
			if !x.batchMaterialIDs(t, batch)[late] {
				t.Fatal("手动检查没有读取完成后新增的材料")
			}
			if associatedRound {
				// 已登记的手动批次可能带有完成轮次，材料范围仍由 scope 决定。
				x.f.Exec(t, `UPDATE supervision_batch_round SET round_id=$2 WHERE batch_id=$1`, batch, completionRound)
			}
			if err := x.svc.Commit(ctx, x.workspace, batch, task, x.batchResult(t, batch)); err != nil {
				t.Fatal(err)
			}
			if !associatedRound && round != "" {
				t.Fatal("新手动批次错误关联了完成快照")
			}
			if n := x.count(t, `SELECT count(*) FROM supervision_material WHERE workspace_id=$1 AND source_id=$2 AND reviewed_generation=generation`, x.workspace, late); n != 1 {
				t.Fatal("手动检查没有保存新材料的覆盖")
			}
			if n := x.count(t, `SELECT count(*) FROM supervision_frozen_material WHERE workspace_id=$1 AND round_id=$2 AND reviewed_generation>0`, x.workspace, completionRound); n != 0 {
				t.Fatal("手动检查推进了独立完成快照的覆盖")
			}
		})
	}
}

func TestManualReviewRoundReferenceDoesNotCancelOnReopen(t *testing.T) {
	ctx := context.Background()
	x := newRoundFixture(t)
	issue := x.issue(t, "done")
	x.f.Task(t, x.agent, dbfx.Cols{"runtime_id": x.runtime, "issue_id": issue, "status": "completed"})
	if err := x.svc.inventory(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	if err := x.svc.reconcileRounds(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	x.f.Exec(t, `UPDATE supervision_subject SET force_review=true WHERE workspace_id=$1 AND issue_id=$2`, x.workspace, issue)
	x.dispatchOnce(t)
	batch, task, _ := x.runningBatchWithRound(t)
	x.f.Exec(t, `UPDATE supervision_batch_round SET round_id=(SELECT id FROM supervision_round WHERE workspace_id=$2 AND issue_id=$3 AND status='open') WHERE batch_id=$1`, batch, x.workspace, issue)
	x.f.Exec(t, `UPDATE supervision_batch SET status='preparing' WHERE id=$1`, batch)
	x.f.Exec(t, `UPDATE issue SET status='in_progress' WHERE id=$1`, issue)
	if err := x.svc.CloseOpenRound(ctx, x.workspace, issue, RoundCloseReopened); err != nil {
		t.Fatal(err)
	}
	if n := x.count(t, `SELECT count(*) FROM supervision_batch WHERE id=$1 AND status='preparing'`, batch); n != 1 {
		t.Fatal("关闭自动完成轮次取消了手动检查")
	}
	x.f.Exec(t, `UPDATE supervision_batch SET status='running' WHERE id=$1`, batch)
	if err := x.svc.Commit(ctx, x.workspace, batch, task, x.batchResult(t, batch)); err != nil {
		t.Fatal(err)
	}
	if n := x.count(t, `SELECT count(*) FROM supervision_batch WHERE id=$1 AND status='completed'`, batch); n != 1 {
		t.Fatal("手动检查在任务重开后未保存结果")
	}
}

// TestCompletionSnapshotIgnoresPostCompletionComments 验证完成之后的普通评论不进入
// 轮次范围，也不会触发整套重审。
func TestCompletionSnapshotIgnoresPostCompletionComments(t *testing.T) {
	ctx := context.Background()
	x := newRoundFixture(t)
	issue := x.issue(t, "in_progress")
	task := x.f.Task(t, x.agent, dbfx.Cols{"runtime_id": x.runtime, "issue_id": issue, "status": "completed"})
	x.f.Insert(t, "task_message", dbfx.Cols{"task_id": task, "seq": 1, "type": "text", "content": "完成前的执行记录"})
	x.f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", issue)
	if err := x.svc.inventory(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	if err := x.svc.reconcileRounds(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	x.dispatchOnce(t)
	var batch, reviewTask string
	if err := x.pool.QueryRow(ctx, `SELECT id::text,COALESCE(task_id::text,'') FROM supervision_batch WHERE workspace_id=$1 AND status='running'`, x.workspace).Scan(&batch, &reviewTask); err != nil {
		t.Fatal(err)
	}
	if err := x.svc.Commit(ctx, x.workspace, batch, reviewTask, x.batchResult(t, batch)); err != nil {
		t.Fatal(err)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$2 AND reviewed_version>=desired_version", x.workspace, issue); n != 1 {
		t.Fatal("首个批次没有完成覆盖")
	}

	// 8. 完成后的普通评论登记为材料，但晚于截止时间，不产生新的自动批次。
	x.f.Comment(t, issue, "完成之后的补充说明")
	x.dispatchOnce(t)
	if n := x.count(t, "SELECT count(*) FROM supervision_batch WHERE workspace_id=$1 AND status='running'", x.workspace); n != 0 {
		t.Fatal("完成后的评论触发了新的自动审查")
	}
	var pending int
	if err := x.pool.QueryRow(ctx, `SELECT count(*) FROM supervision_material m JOIN supervision_round r ON r.workspace_id=m.workspace_id AND r.issue_id=m.issue_id AND r.status='open'
      WHERE m.workspace_id=$1 AND m.generation>m.reviewed_generation AND m.changed_at<=r.material_cutoff`, x.workspace).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("完成后的材料进入了本轮范围：%d", pending)
	}
}
