package handler

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestStageProgressSummarySeparatesCancelledFromDone(t *testing.T) {
	children := []db.Issue{
		child(1, "done"),
		child(2, "cancelled"), child(2, "cancelled"),
		child(3, "backlog"),
	}

	summary, next := stageProgressSummary(children, 2, literalChildStatus)
	want := "阶段 1：已完成 1/1; 阶段 2：已完成 0/2，已取消 2; 阶段 3：已完成 0/1（下一阶段）"
	if summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
	if next != 3 {
		t.Fatalf("nextStage = %d, want 3", next)
	}
}

func TestResolvedChildStatusesKeepCanonicalCancelled(t *testing.T) {
	doneID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	cancelledID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	children := []db.Issue{
		{ID: doneID, Status: "approved", Stage: pgtype.Int4{Int32: 1, Valid: true}},
		{ID: cancelledID, Status: "wont_do", Stage: pgtype.Int4{Int32: 1, Valid: true}},
	}

	statuses, err := resolveChildStatuses(children, func(c db.Issue) (string, error) {
		switch c.Status {
		case "approved":
			return "done", nil
		case "wont_do":
			return "cancelled", nil
		default:
			return c.Status, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses.status(children[1]); got != "cancelled" {
		t.Fatalf("canonical status = %q, want cancelled", got)
	}
	if !statuses.isTerminal(children[1]) {
		t.Fatal("canonical cancelled status must still close the barrier")
	}

	summary, _ := stageProgressSummary(children, 1, statuses.status)
	if want := "阶段 1：已完成 1/2，已取消 1"; summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
}

func TestBatchClosedScopeHasCancelled(t *testing.T) {
	stage2Cancelled := child(2, "cancelled")
	stage2Cancelled.ID = pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	stage7Done := child(7, "done")
	stage7Done.ID = pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	stage20Cancelled := child(20, "cancelled")
	stage20Cancelled.ID = pgtype.UUID{Bytes: [16]byte{20}, Valid: true}
	children := []db.Issue{stage2Cancelled, stage7Done, stage20Cancelled}
	statusOf := func(c db.Issue) string { return c.Status }

	t.Run("lower stage cancelled in the same batch warns", func(t *testing.T) {
		if !batchClosedScopeHasCancelled(children, []db.Issue{stage2Cancelled, stage7Done}, 7, statusOf) {
			t.Fatal("expected cancellation in a lower stage closed by the same batch to be reported")
		}
	})

	t.Run("later unopened stage cancellation is ignored", func(t *testing.T) {
		if batchClosedScopeHasCancelled(children, []db.Issue{stage7Done, stage20Cancelled}, 7, statusOf) {
			t.Fatal("a cancellation above the highest closed stage must not affect the current advance decision")
		}
	})

	t.Run("historical lower-stage cancellation is not repeated", func(t *testing.T) {
		if batchClosedScopeHasCancelled(children, []db.Issue{stage7Done}, 7, statusOf) {
			t.Fatal("a lower-stage cancellation outside this batch must not re-trigger the warning")
		}
	})
}

// A batch can close lower stages alongside the one the comment names. Those
// carry no count of their own, so the warning stays general rather than
// reporting a number that belongs to no single stage.
func TestStageAdvanceInstructionWarnsOnCancelledWork(t *testing.T) {
	got := stageAdvanceInstruction(3, "parent-id", 0, true)
	for _, want := range []string{
		"下一步是阶段 3",
		"请确认被取消的工作不是后续任务的依赖",
		"先不要推进或创建下一阶段",
		"请留言确认",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("instruction missing %q: %s", want, got)
		}
	}
}

func literalChildStatus(c db.Issue) string { return c.Status }

// When the cancellations are in the stage this comment is about, the agent
// deciding whether to promote gets the two facts it can act on: how many
// sub-issues were cancelled, and which stage has to not depend on them.
func TestStageAdvanceInstructionNamesCountAndDependentStage(t *testing.T) {
	got := stageAdvanceInstruction(3, "parent-id", 2, true)
	for _, want := range []string{
		"下一步是阶段 3",
		"有 2 个子任务已取消",
		"请确认阶段 3 不依赖被取消的工作",
		"请留言确认",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("instruction missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "包含已取消项") {
		t.Fatalf("named stage must not fall back to the general warning: %s", got)
	}
}

func TestStageAdvanceInstructionSingularCancellation(t *testing.T) {
	got := stageAdvanceInstruction(3, "parent-id", 1, true)
	if !strings.Contains(got, "有 1 个子任务已取消") {
		t.Fatalf("want singular sub-issue, got %q", got)
	}
	if strings.Contains(got, "1 sub-issues") {
		t.Fatalf("plural leaked into the singular case: %s", got)
	}
}

// The headline above this instruction calls a stage with cancelled work
// `closed`, not complete. With no next stage the instruction opens the very
// next sentence, so it has to use the same word.
func TestStageAdvanceInstructionSaysClosingWhenStageWasCancelled(t *testing.T) {
	got := stageAdvanceInstruction(0, "parent-id", 2, true)
	if !strings.Contains(got, "结束当前阶段不代表") {
		t.Fatalf("want Closing to match the headline, got %q", got)
	}
	if strings.Contains(got, "完成当前阶段") {
		t.Fatalf("stage closed with cancelled work must not read as completed: %s", got)
	}
}

// A lower stage cancelled by the same batch must not change the verb: the
// stage this comment names did complete.
func TestStageAdvanceInstructionKeepsCompletingForBatchScopeOnly(t *testing.T) {
	got := stageAdvanceInstruction(0, "parent-id", 0, true)
	if !strings.Contains(got, "完成当前阶段不代表") {
		t.Fatalf("named stage completed, want Completing, got %q", got)
	}
}
