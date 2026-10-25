package supervision

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbfx "github.com/multica-ai/multica/server/internal/testutil"
)

// 1. Verify real SQL, frozen runtime identity, pagination, replay and workspace isolation.
func TestTraceMaterialLifecycle(t *testing.T) {
	x := newRoundFixture(t)
	ctx := context.Background()
	x.f.Cleanup(t, "DELETE FROM supervision_trace_material WHERE workspace_id=$1", x.workspace)
	issue := x.issue(t, "done")
	run := x.f.Task(t, x.agent, dbfx.Cols{"issue_id": issue, "runtime_id": x.runtime, "status": "completed", "session_id": "session-owned"})
	x.f.Insert(t, "task_trace_event", dbfx.Cols{"workspace_id": x.workspace, "task_id": run, "agent_id": x.agent, "issue_id": issue, "event_type": "task.started", "event_name": "started", "provider": "codex"})
	x.f.Exec(t, "UPDATE agent_runtime SET provider='codebuddy' WHERE id=$1", x.runtime)
	round, err := x.svc.OpenRound(ctx, x.workspace, issue, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = x.svc.prepareTraceMaterials(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	var id, status, runtime, report string
	x.f.QueryRow(t, "SELECT id::text,status,runtime,report FROM supervision_trace_material WHERE workspace_id=$1 AND round_id=$2 AND run_id=$3", x.workspace, round.ID, run).Scan(&id, &status, &runtime, &report)
	if status != "disabled" || runtime != "codex" {
		t.Fatalf("unexpected status/runtime %s/%s", status, runtime)
	}
	page, err := x.svc.ReadEvidence(ctx, x.workspace, "trace_usage", id, 0)
	if err != nil || page.Deleted || page.IssueID != issue || page.RunID != run || !strings.Contains(page.Text, "session-owned") {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	other, err := x.svc.ReadEvidence(ctx, "00000000-0000-0000-0000-000000000000", "trace_usage", id, 0)
	if err != nil || !other.Deleted || other.Text != "" {
		t.Fatalf("cross workspace leaked: %+v %v", other, err)
	}
	if err = x.svc.prepareTraceMaterials(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_trace_material WHERE workspace_id=$1 AND round_id=$2 AND run_id=$3", x.workspace, round.ID, run); n != 1 {
		t.Fatalf("replay duplicated %d", n)
	}
	var after string
	x.f.QueryRow(t, "SELECT report FROM supervision_trace_material WHERE id=$1", id).Scan(&after)
	if after != report {
		t.Fatal("published report mutated on replay")
	}
	if err = x.svc.CloseOpenRound(ctx, x.workspace, issue, RoundCloseReopened); err != nil {
		t.Fatal(err)
	}
	if err = x.svc.prepareTraceMaterials(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_trace_material WHERE workspace_id=$1", x.workspace); n != 1 {
		t.Fatal("closed round added work")
	}
}

// 2. A process restart reclaims expired leases and exposes unknown runtime as a gap.
func TestTraceMaterialLeaseRecovery(t *testing.T) {
	x := newRoundFixture(t)
	ctx := context.Background()
	x.f.Cleanup(t, "DELETE FROM supervision_trace_material WHERE workspace_id=$1", x.workspace)
	issue := x.issue(t, "done")
	run := x.f.Task(t, x.agent, dbfx.Cols{"issue_id": issue, "runtime_id": x.runtime, "status": "failed"})
	round, err := x.svc.OpenRound(ctx, x.workspace, issue, true)
	if err != nil {
		t.Fatal(err)
	}
	id := x.f.Insert(t, "supervision_trace_material", dbfx.Cols{"id": dbfx.Raw("gen_random_uuid()"), "workspace_id": x.workspace, "issue_id": issue, "round_id": round.ID, "run_id": run, "source": "execution", "status": "processing", "lease_until": time.Now().Add(-time.Minute)})
	if err = x.svc.prepareTraceMaterials(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	var status, report string
	var attempts int
	x.f.QueryRow(t, "SELECT status,report,attempts FROM supervision_trace_material WHERE id=$1", id).Scan(&status, &report, &attempts)
	if status != "partial" || attempts != 1 || !strings.Contains(report, "Historical runtime identity unavailable") {
		t.Fatalf("recovery failed %s/%d/%s", status, attempts, report)
	}
}

// 3. One round shares its persisted allowance across workers, runs and service restarts.
func TestTraceRoundBudget(t *testing.T) {
	x := newRoundFixture(t)
	ctx := context.Background()
	issue := x.issue(t, "done")
	round, err := x.svc.OpenRound(ctx, x.workspace, issue, true)
	if err != nil {
		t.Fatal(err)
	}
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 55; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if x.svc.reserveTraceRequest(ctx, x.workspace, round.ID) == nil {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 40 {
		t.Fatalf("allowed %d requests", allowed.Load())
	}
	restarted := *x.svc
	if restarted.reserveTraceRequest(ctx, x.workspace, round.ID) == nil {
		t.Fatal("restart replenished consumed quota")
	}
	x.f.Exec(t, `UPDATE supervision_round SET preparation_http_requests=1,preparation_started_at=now()-interval '121 seconds' WHERE id=$1`, round.ID)
	if restarted.reserveTraceRequest(ctx, x.workspace, round.ID) == nil {
		t.Fatal("recovery reset expired round clock")
	}
}

// 4. A stale claim cannot overwrite a replacement worker's processing row or publish evidence.
func TestTraceMaterialStaleClaim(t *testing.T) {
	x := newRoundFixture(t)
	ctx := context.Background()
	x.f.Cleanup(t, "DELETE FROM supervision_trace_material WHERE workspace_id=$1", x.workspace)
	issue := x.issue(t, "done")
	run := x.f.Task(t, x.agent, dbfx.Cols{"issue_id": issue, "runtime_id": x.runtime, "status": "completed"})
	round, err := x.svc.OpenRound(ctx, x.workspace, issue, true)
	if err != nil {
		t.Fatal(err)
	}
	id := x.f.Insert(t, "supervision_trace_material", dbfx.Cols{"id": dbfx.Raw("gen_random_uuid()"), "workspace_id": x.workspace, "issue_id": issue, "round_id": round.ID, "run_id": run, "source": "execution", "status": "processing", "attempts": 2, "report": "replacement worker", "lease_until": time.Now().Add(time.Minute)})
	stale := tracePreparation{ID: id, Workspace: x.workspace, Issue: issue, Round: round.ID, Run: run, Runtime: "codex", Attempts: 1, Version: 1}
	if err = x.svc.prepareTrace(ctx, stale); err != nil {
		t.Fatal(err)
	}
	var status, report string
	x.f.QueryRow(t, "SELECT status,report FROM supervision_trace_material WHERE id=$1", id).Scan(&status, &report)
	if status != "processing" || report != "replacement worker" {
		t.Fatalf("stale claim overwrote new worker: %s/%s", status, report)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_material WHERE source_id=$1 AND kind='trace_usage'", id); n != 0 {
		t.Fatal("stale worker published evidence")
	}
}

// 1. Manual investigation has one frozen scope and never opens an automatic completion round.
func TestTraceManualPreparationScope(t *testing.T) {
	t.Setenv("MULTICA_SUPERVISION_TRACE_PREPARATION_SINCE", "2026-01-01T00:00:00Z")
	x := newRoundFixture(t)
	ctx := context.Background()
	x.f.Cleanup(t, "DELETE FROM supervision_trace_material WHERE workspace_id=$1", x.workspace)
	x.f.Cleanup(t, "DELETE FROM supervision_manual_preparation WHERE workspace_id=$1", x.workspace)
	issue := x.issue(t, "in_progress")
	run := x.f.Task(t, x.agent, dbfx.Cols{"issue_id": issue, "runtime_id": x.runtime, "status": "completed"})
	x.f.Task(t, x.agent, dbfx.Cols{"issue_id": issue, "runtime_id": x.runtime, "status": "completed", "context": map[string]any{"type": "quality_supervision"}})
	for i := 0; i < 2; i++ {
		tx, err := x.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = PrepareManualMaterials(ctx, tx, x.workspace, issue); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_round WHERE workspace_id=$1 AND issue_id=$2", x.workspace, issue); n != 0 {
		t.Fatal("manual investigation opened an automatic round")
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_trace_material WHERE workspace_id=$1 AND run_id=$2", x.workspace, run); n != 1 {
		t.Fatalf("manual replay duplicated jobs: %d", n)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_trace_material WHERE workspace_id=$1 AND issue_id=$2", x.workspace, issue); n != 1 {
		t.Fatal("supervision created evidence for itself")
	}
	if err := x.svc.prepareTraceMaterials(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_material WHERE workspace_id=$1 AND issue_id=$2 AND kind='trace_usage'", x.workspace, issue); n != 1 {
		t.Fatal("manual evidence not published")
	}
	x.f.Exec(t, "UPDATE supervision_config SET investigation_enabled=true WHERE workspace_id=$1", x.workspace)
	tx, err := x.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	materials, err := x.svc.materials(ctx, tx, x.workspace, issue, noCutoff)
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundIndex := false
	for _, m := range materials {
		if m.Kind == "trace_usage" {
			foundIndex = true
			if !m.IndexOnly || m.End != m.Offset || strings.Contains(m.Text, "local_session_evidence") || ContiguousEnd(m.Offset, []Material{m}) != m.Offset {
				t.Fatalf("index claimed body coverage: %+v", m)
			}
		}
	}
	if !foundIndex {
		t.Fatal("prepared material index missing")
	}
	var scope string
	x.f.QueryRow(t, "SELECT id::text FROM supervision_manual_preparation WHERE workspace_id=$1 AND issue_id=$2", x.workspace, issue).Scan(&scope)
	for i := 0; i < 40; i++ {
		if err := x.svc.reserveTraceRequest(ctx, x.workspace, scope); err != nil {
			t.Fatal(err)
		}
	}
	if x.svc.reserveTraceRequest(ctx, x.workspace, scope) == nil {
		t.Fatal("manual scope exceeded round budget")
	}
}
