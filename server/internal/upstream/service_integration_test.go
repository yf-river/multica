package upstream

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func isolatedService(t *testing.T) *Service {
	t.Helper()
	raw := os.Getenv("UPSTREAM_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set UPSTREAM_TEST_DATABASE_URL to an isolated upstream_* database")
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "upstream_") {
		t.Fatal("integration test requires an explicitly isolated upstream_* database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := "upstream_test_" + strings.ReplaceAll(newID(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"upstream_project", "agent_runtime", "agent_task_queue", "agent", "autopilot", "autopilot_trigger", "upstream_sync_config", "upstream_sync_settings", "upstream_sync_run", "upstream_sync_item", "upstream_sync_commit", "upstream_sync_absorption_task"} {
		if _, err = admin.Exec(ctx, fmt.Sprintf("CREATE TABLE %s.%s (LIKE public.%s INCLUDING ALL)", schema, table, table)); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	return New(pool)
}

// runningTask 插入一条正在执行的任务行。批次的可写条件要求领取它的任务仍在执行，
// 所以几乎所有用例都要先有一条真的任务行，而不是随手编一个 UUID。
func runningTask(t *testing.T, s *Service, runtimeID, status string) string {
	t.Helper()
	if runtimeID == "" {
		runtimeID = daemonRuntime(t, s, "daemon-"+newID())
	}
	id := newID()
	if _, err := s.Pool.Exec(context.Background(), `INSERT INTO agent_task_queue(id,agent_id,status,runtime_id) VALUES($1,$2,$3,$4)`, id, newID(), status, runtimeID); err != nil {
		t.Fatal(err)
	}
	return id
}

// daemonRuntime 插入一条绑定到指定 daemon 的运行时行，用来核对续租只认托管该任务的那台机器。
func daemonRuntime(t *testing.T, s *Service, daemonID string) string {
	t.Helper()
	id := newID()
	if _, err := s.Pool.Exec(context.Background(), `INSERT INTO agent_runtime(id,workspace_id,daemon_id,name,runtime_mode,provider) VALUES($1,$2,$3,'r','local','codex')`, id, newID(), daemonID); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBatchCoverageRetryAndAdoptedState(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	agent := newID()
	_, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton,agent_id,sync_state) VALUES(1,$1,'bootstrap')`, agent)
	if err != nil {
		t.Fatal(err)
	}
	task := runningTask(t, s, "", "running")
	run, err := s.Start(ctx, task, agent, "schedule")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Start(ctx, runningTask(t, s, "", "running"), agent, "manual"); err != ErrBusy {
		t.Fatalf("second batch: %v", err)
	}
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	manifest := BatchManifest{BaseSHA: strings.Repeat("c", 40), HeadSHA: b, Commits: []CommitRef{{SHA: a}, {SHA: b}}, HasMore: false}
	if err = s.Prepare(ctx, run.RunID, manifest); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, run.RunID, b, false); err == nil {
		t.Fatal("unreported batch advanced")
	}
	rep := baseReport()
	rep.Commits = []CommitRef{{SHA: a}}
	rep.Items[0].CommitSHAs = []string{a}
	if _, err = s.AppendReport(ctx, run.RunID, rep); err != nil {
		t.Fatal(err)
	}
	// 首次回传必须立刻关联，重复回传不得重复建条目或改变人工选择。
	_, err = s.Pool.Exec(ctx, `UPDATE upstream_sync_item SET status='adopted'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendReport(ctx, run.RunID, rep); err != nil {
		t.Fatal(err)
	}
	var count int
	var status string
	if err = s.Pool.QueryRow(ctx, `SELECT commit_count,status FROM upstream_sync_item`).Scan(&count, &status); err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != "adopted" {
		t.Fatalf("first/retry association: %d %s", count, status)
	}
	bad := rep
	bad.Commits = []CommitRef{{SHA: strings.Repeat("d", 40)}}
	bad.Items = nil
	if _, err = s.AppendReport(ctx, run.RunID, bad); err == nil {
		t.Fatal("out of scope commit accepted")
	}
	rep.Commits = []CommitRef{{SHA: b}}
	rep.Items[0].CommitSHAs = []string{b}
	if _, err = s.AppendReport(ctx, run.RunID, rep); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, run.RunID, a, false); err == nil {
		t.Fatal("wrong cursor accepted")
	}
	if err = s.Finish(ctx, run.RunID, "", true); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM upstream_sync_commit WHERE item_id IS NOT NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("commit links: %d %v", count, err)
	}
	next, err := s.Start(ctx, runningTask(t, s, "", "running"), agent, "schedule")
	if err != nil || len(next.Processed) != 2 {
		t.Fatalf("completed coverage: %+v %v", next, err)
	}
	if err = s.Heartbeat(ctx, run.RunID); err == nil {
		t.Fatal("completed lease renewed")
	}
}

func TestSettingsKeepSingleExecutorWithoutAutomaticSchedule(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	ws, creator := newID(), newID()
	ids := []string{newID(), newID()}
	for _, id := range ids {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO agent_runtime(id,workspace_id,name,runtime_mode,provider) VALUES($1,$2,'test','local','codex')`, id, ws); err != nil {
			t.Fatal(err)
		}
	}
	base := Settings{BatchSize: 200, ScheduleHour: 9, Enabled: true, UpstreamURL: "https://example.invalid/up.git", UpstreamBranch: "main", LocalRef: "origin/personal-studio-upstream", DefaultInstructions: "默认提示词"}
	with := func(runtime string) Settings {
		in := base
		in.RuntimeID = runtime
		return in
	}
	first, pilot, err := s.SaveSettings(ctx, with(ids[0]), creator)
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	if err = s.Pool.QueryRow(ctx, `SELECT owner_id::text FROM agent WHERE id=$1`, first).Scan(&owner); err != nil || owner != creator {
		t.Fatalf("private executor owner: %s %v", owner, err)
	}
	again, samePilot, err := s.SaveSettings(ctx, with(ids[0]), creator)
	if err != nil || first != again || pilot != samePilot {
		t.Fatalf("duplicate binding %s %s %v", again, samePilot, err)
	}
	// 暂停后恢复：同一执行体、同一自动化，调度重新排到下一个整点。
	if _, _, err = s.SaveSettings(ctx, paused(with(ids[0])), creator); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.SaveSettings(ctx, with(ids[0]), creator); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = s.Pool.QueryRow(ctx, `SELECT status FROM autopilot WHERE id=$1`, pilot).Scan(&status); err != nil || status != "active" {
		t.Fatalf("resume executor: %s %v", status, err)
	}
	// 换运行时只改绑定，不新增执行体或自动化。
	switched, samePilot2, err := s.SaveSettings(ctx, with(ids[1]), creator)
	if err != nil {
		t.Fatal(err)
	}
	if switched != first || samePilot2 != pilot {
		t.Fatalf("runtime switch created new rows: %s %s", switched, samePilot2)
	}
	var agentCount, pilotCount, active int
	if err = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent),(SELECT count(*) FROM autopilot),(SELECT count(*) FROM autopilot_trigger WHERE enabled)`).Scan(&agentCount, &pilotCount, &active); err != nil {
		t.Fatal(err)
	}
	if agentCount != 1 || pilotCount != 1 || active != 0 {
		t.Fatalf("duplicated rows: agents=%d pilots=%d triggers=%d", agentCount, pilotCount, active)
	}
	var boundRuntime string
	if err = s.Pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id=$1`, first).Scan(&boundRuntime); err != nil || boundRuntime != ids[1] {
		t.Fatalf("runtime rebind: %s %v", boundRuntime, err)
	}
	// 执行体仍是旧程序认识的形态：kind='system' 且键按运行时派生，升级期间旧程序
	// 按同一个键找到它，不会另建第二条 Agent 和自动化。
	var kind, systemKey string
	if err = s.Pool.QueryRow(ctx, `SELECT kind,system_key FROM agent WHERE id=$1`, first).Scan(&kind, &systemKey); err != nil || kind != "system" || systemKey != s.executorKey() {
		t.Fatalf("executor identity kind=%s key=%s %v", kind, systemKey, err)
	}
	// 没有运行时也能保存：批量、时间与暂停不依赖绑定。
	before, _, _ := s.SaveSettings(ctx, with(ids[1]), creator)
	pausedNoRuntime := paused(base)
	pausedNoRuntime.RuntimeID = ""
	pausedNoRuntime.BatchSize = 50
	laterHour := pausedNoRuntime
	laterHour.ScheduleHour = 23
	kept, keptPilot, err := s.SaveSettings(ctx, laterHour, creator)
	if err != nil || kept != before || keptPilot != pilot {
		t.Fatalf("config-only save dropped executor: %s %s %v", kept, keptPilot, err)
	}
	var hour int
	var enabled int
	if err = s.Pool.QueryRow(ctx, `SELECT schedule_hour,(SELECT count(*) FROM autopilot_trigger WHERE enabled) FROM upstream_sync_settings WHERE singleton=1`).Scan(&hour, &enabled); err != nil || hour != 23 || enabled != 0 {
		t.Fatalf("paused settings: hour=%d enabled=%d %v", hour, enabled, err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT status FROM autopilot WHERE id=$1`, pilot).Scan(&status); err != nil || status != "paused" {
		t.Fatalf("pause executor: %s %v", status, err)
	}
}

func paused(in Settings) Settings {
	in.Enabled = false
	return in
}

// 升级前建立的执行体（旧版按运行时派生的键、kind='system'）必须被直接接管：
// 重复建行会让旧程序与新程序各跑一条，也会撞上 workspace 内的 Agent 名称唯一约束。
func TestLegacyExecutorIsAdoptedWithoutDuplicate(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	ws, creator, runtime, legacy := newID(), newID(), newID(), newID()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO agent_runtime(id,workspace_id,name,runtime_mode,provider) VALUES($1,$2,'legacy','local','codex')`, runtime, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO agent(id,workspace_id,name,description,avatar_url,runtime_mode,runtime_id,
 instructions,visibility,permission_mode,max_concurrent_tasks,kind,system_key,status,owner_id)
 VALUES($1,$2,'上游观察员 · '||$3::uuid::text,'','🛰','local',$3,'','private','private',1,'system',$4,'offline',$5)`,
		legacy, ws, runtime, ExecutorSystemKeyFor(runtime), creator); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton,agent_id,runtime_id,sync_state) VALUES(1,$1,$2,'bootstrap')`, legacy, runtime); err != nil {
		t.Fatal(err)
	}
	// 旧版本没有设置行：保存前按内置默认展示，不要求迁移回填。
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Config.ScheduleHour != DefaultScheduleHour || snap.Config.Instructions != "" || snap.ExecutorState != "bound" {
		t.Fatalf("defaults before settings row: %+v", snap.Config)
	}
	agentID, _, err := s.SaveSettings(ctx, Settings{
		UpstreamURL: "https://example.invalid/up.git", UpstreamBranch: "main", LocalRef: "origin/personal-studio-upstream",
		BatchSize: 200, ScheduleHour: 10, Enabled: true, RuntimeID: runtime, DefaultInstructions: "默认提示词",
	}, creator)
	if err != nil {
		t.Fatal(err)
	}
	var agents int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM agent`).Scan(&agents); err != nil || agents != 1 {
		t.Fatalf("legacy executor duplicated: agents=%d %v", agents, err)
	}
	if agentID != legacy {
		t.Fatalf("legacy executor replaced: %s", agentID)
	}
	var hour int
	if err := s.Pool.QueryRow(ctx, `SELECT schedule_hour FROM upstream_sync_settings WHERE singleton=1`).Scan(&hour); err != nil || hour != 10 {
		t.Fatalf("settings row not written: hour=%d %v", hour, err)
	}
}

// 换运行时如果目标运行时上还留着旧版建的执行体，只保留配置绑定的那条持有该键：
// 两条 Agent 共用一个 system_key 会让按旧键读取的程序拿到不确定的行。
func TestRebindRetiresLeftoverLegacyExecutor(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	ws, creator := newID(), newID()
	first, second := newID(), newID()
	for _, runtime := range []string{first, second} {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO agent_runtime(id,workspace_id,name,runtime_mode,provider) VALUES($1,$2,'r','local','codex')`, runtime, ws); err != nil {
			t.Fatal(err)
		}
	}
	leftover := newID()
	base := Settings{UpstreamURL: "https://example.invalid/up.git", UpstreamBranch: "main", LocalRef: "origin/personal-studio-upstream",
		BatchSize: 200, ScheduleHour: 9, Enabled: true, DefaultInstructions: "默认提示词"}
	with := func(runtime string) Settings {
		in := base
		in.RuntimeID = runtime
		return in
	}
	bound, _, err := s.SaveSettings(ctx, with(first), creator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO agent(id,workspace_id,name,description,avatar_url,runtime_mode,runtime_id,
 instructions,visibility,permission_mode,max_concurrent_tasks,kind,system_key,status,owner_id)
 VALUES($1,$2,'上游观察员 · '||$3::uuid::text,'','🛰','local',$3,'','private','private',1,'system',$4,'offline',$5)`,
		leftover, ws, second, ExecutorSystemKeyFor(second), creator); err != nil {
		t.Fatal(err)
	}
	rebound, _, err := s.SaveSettings(ctx, with(second), creator)
	if err != nil || rebound != bound {
		t.Fatalf("rebind created a new executor: %s %v", rebound, err)
	}
	var key, leftoverKey string
	if err := s.Pool.QueryRow(ctx, `SELECT system_key FROM agent WHERE id=$1`, bound).Scan(&key); err != nil || key != s.executorKey() {
		t.Fatalf("bound executor key %q %v", key, err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT system_key FROM agent WHERE id=$1`, leftover).Scan(&leftoverKey); err != nil || leftoverKey != ExecutorSystemKeyFor(second) {
		t.Fatalf("leftover executor not retired: %q %v", leftoverKey, err)
	}
	if !IsExecutorSystemKey(leftoverKey) {
		t.Fatalf("legacy executor was unexpectedly changed: %q", leftoverKey)
	}
}

func TestExpiredLeaseCannotAdvanceAndPartialBatchIsRetried(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	agent := newID()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton,agent_id,sync_state) VALUES(1,$1,'bootstrap')`, agent); err != nil {
		t.Fatal(err)
	}
	run, err := s.Start(ctx, runningTask(t, s, "", "running"), agent, "schedule")
	if err != nil {
		t.Fatal(err)
	}
	rep := baseReport()
	manifest := BatchManifest{BaseSHA: strings.Repeat("c", 40), HeadSHA: rep.Commits[0].SHA, Commits: rep.Commits}
	if err = s.Prepare(ctx, run.RunID, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendReport(ctx, run.RunID, rep); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE upstream_sync_run SET lease_expires_at=now()-interval '1 minute' WHERE id=$1`, run.RunID); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, run.RunID, "", false); err == nil {
		t.Fatal("expired lease advanced")
	}
	if _, err = s.AppendReport(ctx, run.RunID, rep); err == nil {
		t.Fatal("expired lease accepted report")
	}
	retry, err := s.Start(ctx, runningTask(t, s, "", "running"), agent, "schedule")
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.Processed) != 0 {
		t.Fatal("unfinished batch treated as completed")
	}
	if err = s.Prepare(ctx, retry.RunID, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendReport(ctx, retry.RunID, rep); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, retry.RunID, "", false); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentStartsHaveOneOwner(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	agent := newID()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton,agent_id,sync_state) VALUES(1,$1,'bootstrap')`, agent); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 5)
	for i := 0; i < 5; i++ {
		task := runningTask(t, s, "", "running")
		go func() { _, err := s.Start(ctx, task, agent, "schedule"); results <- err }()
	}
	accepted := 0
	for i := 0; i < 5; i++ {
		err := <-results
		if err == nil {
			accepted++
		} else if err != ErrBusy {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("expected one owner, got %d", accepted)
	}
}

func TestRunHistoryPaginationKeepsOlderBatches(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	for i := 0; i < 31; i++ {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_run(id,status,created_at) VALUES($1,'completed',now()-$2*interval '1 second')`, newID(), i); err != nil {
			t.Fatal(err)
		}
	}
	first, total, err := s.ListRuns(ctx, 30, 0)
	if err != nil {
		t.Fatal(err)
	}
	older, _, err := s.ListRuns(ctx, 30, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 30 || len(older) != 1 || total != 31 {
		t.Fatalf("history pages=%d/%d total=%d", len(first), len(older), total)
	}
	seen := map[string]bool{}
	for _, run := range append(first, older...) {
		if seen[run.ID] {
			t.Fatal("duplicated batch across pages")
		}
		seen[run.ID] = true
	}
}

// 建议列表同时读取人工选择和关联任务状态，不能混用两张表的同名列。
func TestListItemsIncludesAbsorptionTaskWithoutAmbiguousColumns(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	id, run, issue := newID(), newID(), newID()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_item(id,run_id,cluster_key,title,summary,rationale,verdict,confidence,presence) VALUES($1,$2,'list-regression','有实际价值的改进','摘要','依据','review','medium','partial')`, id, run); err != nil {
		t.Fatal(err)
	}
	items, total, err := s.ListItems(ctx, ItemFilter{})
	if err != nil || len(items) != 1 || total != 1 {
		t.Fatalf("unlinked list: %+v %v", items, err)
	}
	if items[0].AbsorptionIssueID != "" || items[0].AbsorptionStatus != "none" {
		t.Fatalf("unexpected link: %+v", items[0])
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_absorption_task(item_id,issue_id) VALUES($1,$2)`, id, issue); err != nil {
		t.Fatal(err)
	}
	items, total, err = s.ListItems(ctx, ItemFilter{})
	if err != nil || len(items) != 1 || total != 1 || items[0].AbsorptionIssueID != issue || items[0].AbsorptionStatus != "task_created" {
		t.Fatalf("linked list: %+v %v", items, err)
	}
}

// 运行时被删除后执行体整行消失，但配置里记着的自动化还在。此时从设置页点「暂停」
// 必须能停掉它，并且不能把 autopilot_id 写成 NULL——否则这条自动化在页面上再也
// 找不到，触发器却继续按天计时。
func TestPauseReachesAutopilotWithoutExecutor(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	ws, creator, runtime := newID(), newID(), newID()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO agent_runtime(id,workspace_id,name,runtime_mode,provider) VALUES($1,$2,'r','local','codex')`, runtime, ws); err != nil {
		t.Fatal(err)
	}
	base := Settings{UpstreamURL: "https://example.invalid/up.git", UpstreamBranch: "main", LocalRef: "origin/personal-studio-upstream",
		BatchSize: 200, ScheduleHour: 9, Enabled: true, RuntimeID: runtime, DefaultInstructions: "默认提示词"}
	agentID, pilot, err := s.SaveSettings(ctx, base, creator)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟运行时拆除：系统执行体被整行删掉，自动化和配置留在库里。
	if _, err := s.Pool.Exec(ctx, `DELETE FROM agent WHERE id=$1`, agentID); err != nil {
		t.Fatal(err)
	}
	pause := base
	pause.RuntimeID = ""
	pause.Enabled = false
	savedAgent, savedPilot, err := s.SaveSettings(ctx, pause, creator)
	if err != nil {
		t.Fatal(err)
	}
	if savedAgent != "" || savedPilot != pilot {
		t.Fatalf("executor/pilot after pause: %q %q", savedAgent, savedPilot)
	}
	var status, configured string
	var enabled bool
	if err := s.Pool.QueryRow(ctx, `SELECT status FROM autopilot WHERE id=$1`, pilot).Scan(&status); err != nil || status != "paused" {
		t.Fatalf("orphan autopilot still active: %s %v", status, err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(autopilot_id::text,'') FROM upstream_sync_config WHERE singleton=1`).Scan(&configured); err != nil || configured != pilot {
		t.Fatalf("autopilot reference dropped: %q %v", configured, err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM autopilot_trigger WHERE autopilot_id=$1 AND enabled)`, pilot).Scan(&enabled); err != nil || enabled {
		t.Fatalf("orphan trigger still enabled: %v %v", enabled, err)
	}
}

// startedRun 领一批并冻结清单，供租约与收敛用例直接回写。
func startedRun(t *testing.T, s *Service, task, agent string) *StartResult {
	t.Helper()
	ctx := context.Background()
	run, err := s.Start(ctx, task, agent, "manual")
	if err != nil {
		t.Fatal(err)
	}
	rep := baseReport()
	if err = s.Prepare(ctx, run.RunID, BatchManifest{BaseSHA: strings.Repeat("c", 40), HeadSHA: rep.Commits[0].SHA, Commits: rep.Commits}); err != nil {
		t.Fatal(err)
	}
	return run
}

func leaseDeadline(t *testing.T, s *Service, runID string) time.Time {
	t.Helper()
	var deadline time.Time
	if err := s.Pool.QueryRow(context.Background(), `SELECT lease_expires_at FROM upstream_sync_run WHERE id=$1`, runID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	return deadline
}

// TestTaskStatusPollKeepsLongExecutionWithinLease 覆盖"有效长任务不会丢租约"：租约只有
// 2 秒，执行远超这个时长，本任务自己的状态轮询持续到达，批次始终可写并最终正常收尾。
func TestTaskStatusPollKeepsLongExecutionWithinLease(t *testing.T) {
	s := isolatedService(t)
	s.LeaseTTL = 2 * time.Second
	ctx := context.Background()
	agent := newID()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton,agent_id,sync_state) VALUES(1,$1,'bootstrap')`, agent); err != nil {
		t.Fatal(err)
	}
	daemon := "daemon-" + newID()
	task := runningTask(t, s, daemonRuntime(t, s, daemon), "running")
	run := startedRun(t, s, task, agent)

	start := time.Now()
	renewals := 0
	for time.Since(start) < 3*time.Second {
		renewed, err := s.RenewLeaseForPolledTask(ctx, task, daemon)
		if err != nil {
			t.Fatal(err)
		}
		if !renewed {
			t.Fatalf("执行仍在继续，第 %d 次轮询没有续租", renewals+1)
		}
		renewals++
		time.Sleep(400 * time.Millisecond)
	}
	if renewals < 5 {
		t.Fatalf("轮询次数不足，无法证明覆盖长执行: %d", renewals)
	}
	if deadline := leaseDeadline(t, s, run.RunID); !deadline.After(time.Now()) {
		t.Fatalf("持续轮询后租约仍然过期: %s", deadline)
	}
	if _, err := s.AppendReport(ctx, run.RunID, baseReport()); err != nil {
		t.Fatalf("有效执行的回写被拒: %v", err)
	}
	if err := s.Finish(ctx, run.RunID, "", false); err != nil {
		t.Fatalf("有效执行无法收尾: %v", err)
	}
}

// TestOtherTaskPollDoesNotRenewStoppedBatch 覆盖关键反例：同 runtime 的另一个任务继续被
// 轮询，目标任务已经停止，旧批次不得被续租，过期后既不能回写也不能被复活。
func TestOtherTaskPollDoesNotRenewStoppedBatch(t *testing.T) {
	s := isolatedService(t)
	s.LeaseTTL = 2 * time.Second
	ctx := context.Background()
	agent := newID()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton,agent_id,sync_state) VALUES(1,$1,'bootstrap')`, agent); err != nil {
		t.Fatal(err)
	}
	daemon := "daemon-" + newID()
	runtime := daemonRuntime(t, s, daemon)
	target := runningTask(t, s, runtime, "running")
	other := runningTask(t, s, runtime, "running")
	run := startedRun(t, s, target, agent)

	for i := 0; i < 6; i++ {
		renewed, err := s.RenewLeaseForPolledTask(ctx, other, daemon)
		if err != nil {
			t.Fatal(err)
		}
		if renewed {
			t.Fatal("同机别的任务续租了不属于它的批次")
		}
		time.Sleep(400 * time.Millisecond)
	}
	if deadline := leaseDeadline(t, s, run.RunID); deadline.After(time.Now()) {
		t.Fatalf("目标任务已停止，租约仍被同机轮询续上: %s", deadline)
	}
	if _, err := s.AppendReport(ctx, run.RunID, baseReport()); !errors.Is(err, ErrRunExpired) {
		t.Fatalf("过期批次回写应报 410 语义，实际: %v", err)
	}
	if err := s.Finish(ctx, run.RunID, "", false); !errors.Is(err, ErrRunExpired) {
		t.Fatalf("过期批次收尾应报 410 语义，实际: %v", err)
	}
	if renewed, err := s.RenewLeaseForPolledTask(ctx, target, daemon); err != nil || renewed {
		t.Fatalf("过期租约被复活: %v %v", renewed, err)
	}
	next, err := s.Start(ctx, runningTask(t, s, "", "running"), agent, "manual")
	if err != nil {
		t.Fatalf("旧批次没有释放活动槽位: %v", err)
	}
	var status, message string
	if err := s.Pool.QueryRow(ctx, `SELECT status,error FROM upstream_sync_run WHERE id=$1`, run.RunID).Scan(&status, &message); err != nil || status != "interrupted" || message != "租约过期" {
		t.Fatalf("旧批次未被回收: %q %q %v", status, message, err)
	}
	if next.RunID == run.RunID {
		t.Fatal("新任务复用了过期批次")
	}
}

// TestLeaseRenewRequiresOwningDaemonAndRunningTask 覆盖续租的身份、归属与状态三个条件：
// 换一台 daemon、调用方不带身份、任务行已经终态都不能续租。
func TestLeaseRenewRequiresOwningDaemonAndRunningTask(t *testing.T) {
	s := isolatedService(t)
	s.LeaseTTL = 10 * time.Minute
	ctx := context.Background()
	agent := newID()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton,agent_id,sync_state) VALUES(1,$1,'bootstrap')`, agent); err != nil {
		t.Fatal(err)
	}
	daemon := "daemon-" + newID()
	task := runningTask(t, s, daemonRuntime(t, s, daemon), "running")
	run := startedRun(t, s, task, agent)
	before := leaseDeadline(t, s, run.RunID)

	for name, daemonID := range map[string]string{"别的 daemon": "daemon-" + newID(), "无身份": ""} {
		renewed, err := s.RenewLeaseForPolledTask(ctx, task, daemonID)
		if err != nil {
			t.Fatal(err)
		}
		if renewed {
			t.Fatalf("%s 续租了不属于它的批次", name)
		}
	}
	if after := leaseDeadline(t, s, run.RunID); !after.Equal(before) {
		t.Fatalf("非归属调用改动了租约: %s -> %s", before, after)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE agent_task_queue SET status='cancelled' WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	if renewed, err := s.RenewLeaseForPolledTask(ctx, task, daemon); err != nil || renewed {
		t.Fatalf("终态任务的批次仍被续租: %v %v", renewed, err)
	}
}

// TestTerminalTaskClosesBatchAndRejectsStaleWrites 覆盖取消、失败和"完成但没回报"三种
// 终态：批次立刻收尾释放活动槽位，旧 run 的回写一律被拒。
func TestTerminalTaskClosesBatchAndRejectsStaleWrites(t *testing.T) {
	cases := []struct {
		name       string
		taskStatus string
		closeAs    string
		wantErr    error
	}{
		{"取消", "cancelled", "interrupted", ErrRunGone},
		{"失败", "failed", "failed", ErrRunGone},
		{"完成未回报", "completed", "interrupted", ErrRunGone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := isolatedService(t)
			ctx := context.Background()
			agent := newID()
			if _, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton,agent_id,sync_state) VALUES(1,$1,'bootstrap')`, agent); err != nil {
				t.Fatal(err)
			}
			daemon := "daemon-" + newID()
			task := runningTask(t, s, daemonRuntime(t, s, daemon), "running")
			run := startedRun(t, s, task, agent)
			if _, err := s.Pool.Exec(ctx, `UPDATE agent_task_queue SET status=$2 WHERE id=$1`, task, tc.taskStatus); err != nil {
				t.Fatal(err)
			}
			closed, err := s.CloseTaskRuns(ctx, task, tc.closeAs, "任务已结束，批次已关闭")
			if err != nil || closed != 1 {
				t.Fatalf("批次没有随任务终态收敛: %d %v", closed, err)
			}
			var status string
			var lease *time.Time
			if err := s.Pool.QueryRow(ctx, `SELECT status,lease_expires_at FROM upstream_sync_run WHERE id=$1`, run.RunID).Scan(&status, &lease); err != nil {
				t.Fatal(err)
			}
			if status != tc.closeAs || lease != nil {
				t.Fatalf("批次终态不正确: %q %v", status, lease)
			}
			if _, err := s.AppendReport(ctx, run.RunID, baseReport()); !errors.Is(err, tc.wantErr) {
				t.Fatalf("旧 run 回写没有被拒: %v", err)
			}
			if err := s.Finish(ctx, run.RunID, "", false); !errors.Is(err, tc.wantErr) {
				t.Fatalf("旧 run 收尾没有被拒: %v", err)
			}
			if _, err := s.RenewLeaseForPolledTask(ctx, task, daemon); err != nil {
				t.Fatal(err)
			}
			next, err := s.Start(ctx, runningTask(t, s, "", "running"), agent, "manual")
			if err != nil {
				t.Fatalf("终态任务仍占着活动槽位: %v", err)
			}
			if next.RunID == run.RunID {
				t.Fatal("新任务复用了已关闭批次")
			}
		})
	}
}
