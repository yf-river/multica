package service

import (
	"context"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/attribution"
)

func TestDelegatedResultResumesCoordinatorOnce(t *testing.T) {
	for _, status := range []string{"completed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f, svc := seedDelegatedFailureFixture(t)
			ctx := context.Background()
			id := f.insertWorkerTask(t, status, "comment", 1, 2)
			task, err := svc.Queries.GetAgentTask(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := svc.RecoverDelegatedTaskResult(ctx, task); err != nil {
					t.Fatal(err)
				}
			}
			var count int
			var kind, issue, agent string
			if err := f.pool.QueryRow(ctx, `SELECT count(*) OVER(),trigger_evidence_kind,issue_id::text,agent_id::text FROM agent_task_queue WHERE trigger_evidence_ref_id=$1`, id).Scan(&count, &kind, &issue, &agent); err != nil {
				t.Fatal(err)
			}
			if count != 1 || kind != string(attribution.EvidenceDelegatedResult) || issue != f.issueID || agent != f.coordinator {
				t.Fatalf("callback count=%d kind=%s issue=%s agent=%s", count, kind, issue, agent)
			}
		})
	}
}

func TestDelegatedResultExcludesPauseAndCallbackLoops(t *testing.T) {
	for _, reason := range []string{"release_pause", "callback"} {
		t.Run(reason, func(t *testing.T) {
			f, svc := seedDelegatedFailureFixture(t)
			ctx := context.Background()
			kind := "comment"
			if reason == "callback" {
				kind = string(attribution.EvidenceDelegatedResult)
			}
			id := f.insertWorkerTask(t, "cancelled", kind, 1, 2)
			if reason == "release_pause" {
				if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET failure_reason='release_pause' WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
			}
			task, err := svc.Queries.GetAgentTask(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if err = svc.RecoverDelegatedTaskResult(ctx, task); err != nil {
				t.Fatal(err)
			}
			var n int
			if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE source_task_id=$1`, id).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("excluded callback created %d comments", n)
			}
		})
	}
}

func TestDelegatedResultReplaysPersistedUndispatchedSignal(t *testing.T) {
	f, svc := seedDelegatedFailureFixture(t)
	ctx := context.Background()
	id := f.insertWorkerTask(t, "completed", "comment", 1, 2)
	if _, created, err := svc.ensureDelegatedFailureRecoveryComment(ctx, id); err != nil || !created {
		t.Fatalf("persist signal: created=%t err=%v", created, err)
	}
	for i := 0; i < 2; i++ {
		if _, err := svc.RecoverPendingDelegatedFailures(ctx, 20); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE trigger_evidence_kind='delegated_result' AND trigger_evidence_ref_id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("replayed %d callbacks", n)
	}
}

func TestDelegatedResultConcurrentReplayCreatesOneSuccessor(t *testing.T) {
	f, svc := seedDelegatedFailureFixture(t)
	ctx := context.Background()
	id := f.insertWorkerTask(t, "completed", "comment", 1, 2)
	task, err := svc.Queries.GetAgentTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- svc.RecoverDelegatedTaskResult(ctx, task) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE trigger_evidence_kind='delegated_result' AND trigger_evidence_ref_id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("concurrent replay created %d successors", n)
	}
}
