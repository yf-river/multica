package handler

import (
	"context"
	"sync"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (b *BatchedHeartbeatScheduler) FlushNow(ctx context.Context) {
	b.flushOnce(ctx)
}

func (b *BatchedHeartbeatScheduler) PendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

func newBatchedHeartbeatRuntime(t *testing.T) (string, db.AgentRuntime, time.Time) {
	t.Helper()
	requireHandlerDatabase(t)
	runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
	stale := time.Now().Add(-2 * time.Hour)
	setRuntimeLastSeenAt(t, runtimeID, stale)
	return runtimeID, loadRuntime(t, runtimeID), stale
}

func scheduleTestHeartbeat(t *testing.T, scheduler *BatchedHeartbeatScheduler, runtime db.AgentRuntime) {
	t.Helper()
	if err := scheduler.Schedule(context.Background(), runtime); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
}

func assertBatchedHeartbeatFlushed(t *testing.T, scheduler *BatchedHeartbeatScheduler, runtimeID string, stale time.Time) {
	t.Helper()
	if got := scheduler.PendingCount(); got != 0 {
		t.Fatalf("expected pending=0 after flush, got %d", got)
	}
	_, lastSeen := readRuntimeRow(t, runtimeID)
	if !lastSeen.After(stale.Add(time.Hour)) {
		t.Fatalf("heartbeat was not flushed: stale=%s after=%s", stale, lastSeen)
	}
}

func TestBatchedHeartbeatScheduler_CoalescesAndFlushes(t *testing.T) {
	runtimeID, runtime, stale := newBatchedHeartbeatRuntime(t)

	sched := NewBatchedHeartbeatScheduler(testHandler.Queries, 0)

	const callers = 50
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			if err := sched.Schedule(context.Background(), runtime); err != nil {
				t.Errorf("Schedule: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := sched.PendingCount(); got != 1 {
		t.Fatalf("expected coalesced pending=1, got %d", got)
	}

	_, lastSeenBefore := readRuntimeRow(t, runtimeID)
	if !lastSeenBefore.Equal(stale) {
		if lastSeenBefore.After(stale.Add(time.Second)) {
			t.Fatalf("DB unexpectedly bumped before flush: %s", lastSeenBefore)
		}
	}

	sched.FlushNow(context.Background())
	assertBatchedHeartbeatFlushed(t, sched, runtimeID, stale)
}

func TestBatchedHeartbeatScheduler_OfflineFallsBackSync(t *testing.T) {
	requireHandlerDatabase(t)
	runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
	setRuntimeOffline(t, runtimeID)
	setRuntimeLastSeenAt(t, runtimeID, time.Now())
	rt := loadRuntime(t, runtimeID)
	if rt.Status != "offline" {
		t.Fatalf("setup: status=%q want offline", rt.Status)
	}

	sched := NewBatchedHeartbeatScheduler(testHandler.Queries, 0)
	scheduleTestHeartbeat(t, sched, rt)

	if got := sched.PendingCount(); got != 0 {
		t.Fatalf("offline row should not have been queued, pending=%d", got)
	}
	status, _ := readRuntimeRow(t, runtimeID)
	if status != "online" {
		t.Fatalf("expected status=online after sync flip, got %q", status)
	}
}

func TestBatchedHeartbeatScheduler_StopFlushesPending(t *testing.T) {
	for _, test := range []struct {
		name                  string
		stopRunBeforeSchedule bool
	}{
		{name: "while run is active"},
		{name: "after run exits", stopRunBeforeSchedule: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtimeID, runtime, stale := newBatchedHeartbeatRuntime(t)
			scheduler := NewBatchedHeartbeatScheduler(testHandler.Queries, time.Hour)
			runContext, cancelRun := context.WithCancel(context.Background())
			t.Cleanup(cancelRun)
			go scheduler.Run(runContext)

			if test.stopRunBeforeSchedule {
				cancelRun()
				time.Sleep(50 * time.Millisecond)
			}
			scheduleTestHeartbeat(t, scheduler, runtime)
			if got := scheduler.PendingCount(); got != 1 {
				t.Fatalf("expected pending=1 before Stop, got %d", got)
			}

			scheduler.Stop()
			assertBatchedHeartbeatFlushed(t, scheduler, runtimeID, stale)
		})
	}
}

func TestBatchedHeartbeatScheduler_FlushIgnoresEmpty(t *testing.T) {
	requireHandlerDatabase(t)
	sched := NewBatchedHeartbeatScheduler(testHandler.Queries, 0)
	sched.FlushNow(context.Background())
	if got := sched.PendingCount(); got != 0 {
		t.Fatalf("pending should remain 0, got %d", got)
	}
}

func TestBatchedHeartbeatScheduler_RaceToOfflineSelfHeals(t *testing.T) {
	requireHandlerDatabase(t)
	runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
	rt := loadRuntime(t, runtimeID)

	sched := NewBatchedHeartbeatScheduler(testHandler.Queries, 0)
	scheduleTestHeartbeat(t, sched, rt)

	setRuntimeOffline(t, runtimeID)

	sched.FlushNow(context.Background())

	status, _ := readRuntimeRow(t, runtimeID)
	if status != "offline" {
		t.Fatalf("expected status=offline after raced flush, got %q", status)
	}

	rt2 := loadRuntime(t, runtimeID)
	scheduleTestHeartbeat(t, sched, rt2)
	status2, _ := readRuntimeRow(t, runtimeID)
	if status2 != "online" {
		t.Fatalf("expected sync recovery to flip back to online, got %q", status2)
	}
}
