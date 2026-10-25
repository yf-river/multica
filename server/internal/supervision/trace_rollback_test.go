package supervision

import (
	"context"
	"os"
	"strings"
	"testing"

	dbfx "github.com/multica-ai/multica/server/internal/testutil"
)

// 1. Run this same fixture against the pinned legacy source and candidate schema:
// rollback must pause supervision without erasing unread evidence or consumed budgets.
func TestTraceRollbackPreservesEvidence(t *testing.T) {
	x := newRoundFixture(t)
	ctx := context.Background()
	issue := x.issue(t, "done")
	run := x.f.Task(t, x.agent, dbfx.Cols{"issue_id": issue, "runtime_id": x.runtime, "status": "completed", "completed_at": dbfx.Raw("now()")})
	round, err := x.svc.OpenRound(ctx, x.workspace, issue, true)
	if err != nil {
		t.Fatal(err)
	}
	x.f.Exec(t, "UPDATE supervision_round SET preparation_http_requests=7,preparation_recheck_scheduled=true WHERE id=$1", round.ID)
	id := x.f.Insert(t, "supervision_trace_material", dbfx.Cols{"id": dbfx.Raw("gen_random_uuid()"), "workspace_id": x.workspace, "issue_id": issue, "round_id": round.ID, "run_id": run, "source": "execution", "status": "partial", "report": "retained immutable evidence"})
	x.f.InsertNoID(t, "supervision_material", dbfx.Cols{"workspace_id": x.workspace, "issue_id": issue, "kind": "trace_usage", "source_id": id, "run_id": run}, "workspace_id=$1 AND source_id=$2", x.workspace, id)
	tx, err := x.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	materials, readErr := x.svc.materials(ctx, tx, x.workspace, issue, noCutoff)
	_ = tx.Rollback(ctx)
	if os.Getenv("MULTICA_TRACE_ROLLBACK_LEGACY") == "1" {
		if readErr == nil || !strings.Contains(readErr.Error(), `unknown material kind "trace_usage"`) {
			t.Fatalf("legacy reader must expose rollback precondition: %v", readErr)
		}
		t.Log("legacy reader rejects trace_usage; supervision must stay paused")
	} else {
		found := false
		for _, material := range materials {
			if material.Kind == "trace_usage" && material.ID == id && material.Text == "retained immutable evidence" {
				found = true
			}
		}
		if readErr != nil || !found {
			t.Fatalf("candidate cannot resume retained evidence: %+v %v", materials, readErr)
		}
	}
	x.f.Exec(t, "UPDATE supervision_config SET enabled=false WHERE workspace_id=$1", x.workspace)
	for i := 0; i < 2; i++ {
		if err = x.svc.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		x.dispatchOnce(t)
	}
	if n := x.count(t, "SELECT count(*) FROM supervision_batch WHERE workspace_id=$1", x.workspace); n != 0 {
		t.Fatalf("paused rollback dispatched %d batches", n)
	}
	// 2. Ordinary legacy reads and writes remain usable while supervision is paused.
	x.f.Exec(t, "UPDATE issue SET title='rollback remains writable' WHERE id=$1", issue)
	query, err := sourceSQL("issue")
	if err != nil {
		t.Fatal(err)
	}
	var body string
	x.f.QueryRow(t, query, issue, x.workspace).Scan(&body)
	if !strings.Contains(body, "rollback remains writable") {
		t.Fatal("ordinary issue read/write failed")
	}
	var report string
	var requests, reviewed, offset int
	var recheck bool
	x.f.QueryRow(t, `SELECT p.report,r.preparation_http_requests,r.preparation_recheck_scheduled,m.reviewed_generation,m.reviewed_offset
FROM supervision_trace_material p JOIN supervision_round r ON r.id=p.round_id
JOIN supervision_material m ON m.source_id=p.id AND m.kind='trace_usage' WHERE p.id=$1`, id).Scan(&report, &requests, &recheck, &reviewed, &offset)
	if report != "retained immutable evidence" || requests != 7 || !recheck || reviewed != 0 || offset != 0 {
		t.Fatalf("rollback changed evidence/budget/progress: %q %d %t %d %d", report, requests, recheck, reviewed, offset)
	}
}
