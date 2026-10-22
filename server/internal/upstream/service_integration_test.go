package upstream

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"os"
	"strings"
	"testing"
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
	for _, table := range []string{"agent_runtime", "agent", "autopilot", "autopilot_trigger", "upstream_sync_config", "upstream_sync_run", "upstream_sync_item", "upstream_sync_commit"} {
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

func TestBatchCoverageRetryAndAdoptedState(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	agent := newID()
	_, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton,agent_id,sync_state) VALUES(1,$1,'bootstrap')`, agent)
	if err != nil {
		t.Fatal(err)
	}
	task := newID()
	run, err := s.Start(ctx, task, agent, "schedule")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Start(ctx, newID(), agent, "manual"); err != ErrBusy {
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
	next, err := s.Start(ctx, newID(), agent, "schedule")
	if err != nil || len(next.Processed) != 2 {
		t.Fatalf("completed coverage: %+v %v", next, err)
	}
	if err = s.Heartbeat(ctx, run.RunID); err == nil {
		t.Fatal("completed lease renewed")
	}
}

func TestSetupIsAtomicAndSwitchDisablesOldSchedule(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	ws, creator := newID(), newID()
	ids := []string{newID(), newID()}
	for _, id := range ids {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO agent_runtime(id,workspace_id,name,runtime_mode,provider) VALUES($1,$2,'test','local','codex')`, id, ws); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{RuntimeID: ids[0], BatchSize: 200, UpstreamURL: "https://example.invalid/up.git", UpstreamBranch: "main", LocalRef: "origin/personal-studio-upstream"}
	first, pilot, err := s.SetupExecutor(ctx, cfg, creator, "instructions", true)
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	if err = s.Pool.QueryRow(ctx, `SELECT owner_id::text FROM agent WHERE id=$1`, first).Scan(&owner); err != nil || owner != creator {
		t.Fatalf("private executor owner: %s %v", owner, err)
	}
	again, same, err := s.SetupExecutor(ctx, cfg, creator, "new instructions", true)
	if err != nil || first != again || pilot != same {
		t.Fatalf("duplicate binding %s %s %v", again, same, err)
	}
	cfg.RuntimeID = ids[1]
	_, newPilot, err := s.SetupExecutor(ctx, cfg, creator, "instructions", true)
	if err != nil {
		t.Fatal(err)
	}
	var active int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM autopilot_trigger WHERE enabled`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("old schedule remains active: %d %v", active, err)
	}
	if newPilot == pilot {
		t.Fatal("runtime switch retained old executor")
	}
	if _, _, err = s.SetupExecutor(ctx, cfg, creator, "instructions", false); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM autopilot_trigger WHERE enabled`).Scan(&active); err != nil || active != 0 {
		t.Fatalf("disable: %d %v", active, err)
	}
}

func TestExpiredLeaseCannotAdvanceAndPartialBatchIsRetried(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	agent := newID()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton,agent_id,sync_state) VALUES(1,$1,'bootstrap')`, agent); err != nil {
		t.Fatal(err)
	}
	run, err := s.Start(ctx, newID(), agent, "schedule")
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
	retry, err := s.Start(ctx, newID(), agent, "schedule")
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
		go func() { _, err := s.Start(ctx, newID(), agent, "schedule"); results <- err }()
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
