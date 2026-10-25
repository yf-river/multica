package supervision

import (
	"context"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	"strings"
	"testing"
)

func TestCrossBeforeCompletionCommentEditIsIncluded(t *testing.T) {
	ctx := context.Background()
	x := newRoundFixture(t)
	issue := x.issue(t, "in_progress")
	comment := x.f.Comment(t, issue, "original")
	x.f.Task(t, x.agent, dbfx.Cols{"runtime_id": x.runtime, "issue_id": issue, "status": "completed"})
	x.f.Exec(t, "UPDATE comment SET content='edited BEFORE completion' WHERE id=$1", comment)
	x.f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", issue)
	if err := x.svc.inventory(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	if err := x.svc.reconcileRounds(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	x.dispatchOnce(t)
	batch, _, _ := x.runningBatchWithRound(t)
	page, err := x.svc.ReadBatchEvidence(ctx, x.workspace, batch, "comment", comment)
	if err != nil || !strings.Contains(page.Text, "edited BEFORE") {
		t.Fatalf("before-completion edit control absent: %v %s", err, page.Text)
	}
}
func TestCrossDispatchedBatchEvidenceRemainsFrozen(t *testing.T) {
	ctx := context.Background()
	x := newRoundFixture(t)
	issue := x.issue(t, "in_progress")
	comment := x.f.Comment(t, issue, "ORIGINAL frozen evidence")
	x.f.Task(t, x.agent, dbfx.Cols{"runtime_id": x.runtime, "issue_id": issue, "status": "completed"})
	x.f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", issue)
	if err := x.svc.inventory(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	if err := x.svc.reconcileRounds(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	x.dispatchOnce(t)
	batch, _, _ := x.runningBatchWithRound(t)
	x.f.Exec(t, "UPDATE comment SET content='edited AFTER dispatch' WHERE id=$1", comment)
	page, err := x.svc.ReadBatchEvidence(ctx, x.workspace, batch, "comment", comment)
	if err != nil || !strings.Contains(page.Text, "ORIGINAL frozen") {
		t.Fatalf("dispatched batch should remain frozen: %v %s", err, page.Text)
	}
}

func TestGong78CompletionFreezesBeforeDispatch(t *testing.T) {
	ctx := context.Background()
	x := newRoundFixture(t)
	issue := x.issue(t, "in_progress")
	comment := x.f.Comment(t, issue, "ORIGINAL before dispatch")
	x.f.Task(t, x.agent, dbfx.Cols{"runtime_id": x.runtime, "issue_id": issue, "status": "completed"})
	x.f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", issue)
	if err := x.svc.inventory(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.OpenRound(ctx, x.workspace, issue, true); err != nil {
		t.Fatal(err)
	}
	x.f.Exec(t, "UPDATE comment SET content='edited AFTER completion BEFORE dispatch' WHERE id=$1", comment)
	x.dispatchOnce(t)
	batch, _, _ := x.runningBatchWithRound(t)
	page, err := x.svc.ReadBatchEvidence(ctx, x.workspace, batch, "comment", comment)
	if err != nil || !strings.Contains(page.Text, "ORIGINAL before dispatch") {
		t.Fatalf("completion evidence changed: %v %s", err, page.Text)
	}
}

func TestCompletionFreezeIncludesPreparedRuntimeUsage(t *testing.T) {
	ctx := context.Background()
	x := newRoundFixture(t)
	x.f.Cleanup(t, "DELETE FROM supervision_trace_material WHERE workspace_id=$1", x.workspace)
	issue := x.issue(t, "in_progress")
	x.f.Task(t, x.agent, dbfx.Cols{"issue_id": issue, "runtime_id": x.runtime, "status": "completed", "completed_at": dbfx.Raw("now()"), "session_id": "frozen-runtime-session"})
	if _, err := x.svc.OpenRound(ctx, x.workspace, issue, false); err != nil {
		t.Fatal(err)
	}
	if err := x.svc.prepareTraceMaterials(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	var material string
	x.f.QueryRow(t, "SELECT id::text FROM supervision_trace_material WHERE workspace_id=$1 AND issue_id=$2", x.workspace, issue).Scan(&material)
	if err := x.svc.CloseOpenRound(ctx, x.workspace, issue, RoundCloseRecompleted); err != nil {
		t.Fatal(err)
	}
	x.f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", issue)
	if err := x.svc.inventory(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	if err := x.svc.reconcileRounds(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	x.dispatchOnce(t)
	batch, _, _ := x.runningBatchWithRound(t)
	page, err := x.svc.ReadBatchEvidence(ctx, x.workspace, batch, "trace_usage", material)
	if err != nil || !strings.Contains(page.Text, "frozen-runtime-session") {
		t.Fatalf("完成快照必须保留已准备的runtime用量材料: %v %+v", err, page)
	}
}

func TestCompletionSnapshotIncludesLatePreparedRuntimeUsage(t *testing.T) {
	ctx := context.Background()
	x := newRoundFixture(t)
	x.f.Cleanup(t, "DELETE FROM supervision_trace_material WHERE workspace_id=$1", x.workspace)
	issue := x.issue(t, "in_progress")
	x.f.Task(t, x.agent, dbfx.Cols{"issue_id": issue, "runtime_id": x.runtime, "status": "completed", "completed_at": dbfx.Raw("now()"), "session_id": "late-frozen-runtime-session"})
	x.f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", issue)
	if err := x.svc.inventory(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.OpenRound(ctx, x.workspace, issue, true); err != nil {
		t.Fatal(err)
	}
	// 模拟真实顺序：完成时冻结原始材料，后台随后派生轨迹报告。
	if err := x.svc.prepareTraceMaterials(ctx, x.workspace); err != nil {
		t.Fatal(err)
	}
	var material string
	x.f.QueryRow(t, "SELECT id::text FROM supervision_trace_material WHERE workspace_id=$1 AND issue_id=$2", x.workspace, issue).Scan(&material)
	x.dispatchOnce(t)
	batch, _, _ := x.runningBatchWithRound(t)
	page, err := x.svc.ReadBatchEvidence(ctx, x.workspace, batch, "trace_usage", material)
	if err != nil || !strings.Contains(page.Text, "late-frozen-runtime-session") {
		t.Fatalf("后台派生报告必须进入原完成轮次且可分页读取: %v %+v", err, page)
	}
}
