package handler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeLivenessStore struct {
	mu          sync.Mutex
	available   bool
	touchErr    error
	touched     []string
	aliveResult map[string]bool
	aliveOK     bool
	forgotten   []string
}

func (f *fakeLivenessStore) Available() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available
}

func (f *fakeLivenessStore) Touch(_ context.Context, runtimeID string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, runtimeID)
	return f.touchErr
}

func (f *fakeLivenessStore) IsAliveBatch(_ context.Context, ids []string) (map[string]bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.aliveOK {
		return nil, false
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = f.aliveResult[id]
	}
	return out, true
}

func (f *fakeLivenessStore) Forget(_ context.Context, runtimeID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, runtimeID)
}

func (f *fakeLivenessStore) touchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.touched)
}

func readRuntimeRow(t *testing.T, runtimeID string) (status string, lastSeen time.Time) {
	t.Helper()
	if err := testPool.QueryRow(context.Background(),
		`SELECT status, last_seen_at FROM agent_runtime WHERE id = $1`, runtimeID,
	).Scan(&status, &lastSeen); err != nil {
		t.Fatalf("read runtime row: %v", err)
	}
	return
}

func setRuntimeLastSeenAt(t *testing.T, runtimeID string, when time.Time) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE agent_runtime SET last_seen_at = $1 WHERE id = $2`, when, runtimeID,
	); err != nil {
		t.Fatalf("force last_seen_at: %v", err)
	}
}

func setRuntimeOffline(t *testing.T, runtimeID string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE agent_runtime SET status = 'offline' WHERE id = $1`, runtimeID,
	); err != nil {
		t.Fatalf("force status: %v", err)
	}
}

func loadRuntime(t *testing.T, runtimeID string) db.AgentRuntime {
	t.Helper()
	rt, err := testHandler.Queries.GetAgentRuntime(context.Background(), parseUUID(runtimeID))
	if err != nil {
		t.Fatalf("GetAgentRuntime: %v", err)
	}
	return rt
}

func useLivenessStore(t *testing.T, store LivenessStore) {
	t.Helper()
	previous := testHandler.LivenessStore
	testHandler.LivenessStore = store
	t.Cleanup(func() { testHandler.LivenessStore = previous })
}

func recordTestHeartbeat(t *testing.T, runtime db.AgentRuntime) {
	t.Helper()
	if err := testHandler.recordHeartbeat(context.Background(), runtime); err != nil {
		t.Fatalf("recordHeartbeat: %v", err)
	}
}

func TestRecordHeartbeat_DBWriteFallbacks(t *testing.T) {
	for _, test := range []struct {
		name  string
		store LivenessStore
	}{
		{name: "unavailable store", store: NewNoopLivenessStore()},
		{name: "touch failure", store: &fakeLivenessStore{available: true, touchErr: errors.New("simulated redis outage")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireHandlerDatabase(t)
			runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
			useLivenessStore(t, test.store)
			setRuntimeLastSeenAt(t, runtimeID, time.Now())
			runtime := loadRuntime(t, runtimeID)
			before := runtime.LastSeenAt.Time
			time.Sleep(50 * time.Millisecond)
			recordTestHeartbeat(t, runtime)
			_, lastSeen := readRuntimeRow(t, runtimeID)
			if !lastSeen.After(before) {
				t.Fatalf("heartbeat did not update DB: before=%s after=%s", before, lastSeen)
			}
		})
	}
}

func TestRecordHeartbeat_RedisAvailableSkipsDBWithinFlushWindow(t *testing.T) {
	requireHandlerDatabase(t)
	runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)

	fake := &fakeLivenessStore{available: true, aliveOK: true}
	useLivenessStore(t, fake)

	setRuntimeLastSeenAt(t, runtimeID, time.Now())
	rt := loadRuntime(t, runtimeID)
	before := rt.LastSeenAt.Time

	recordTestHeartbeat(t, rt)

	if fake.touchCount() != 1 {
		t.Fatalf("expected exactly one Touch, got %d", fake.touchCount())
	}
	_, lastSeen := readRuntimeRow(t, runtimeID)
	if !lastSeen.Equal(before) {
		t.Fatalf("DB last_seen_at should not have been rewritten within flush window: before=%s after=%s", before, lastSeen)
	}
}

func TestRecordHeartbeat_DBFlushOnStaleRow(t *testing.T) {
	requireHandlerDatabase(t)
	runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)

	fake := &fakeLivenessStore{available: true, aliveOK: true}
	useLivenessStore(t, fake)

	stale := time.Now().Add(-2 * runtimeHeartbeatDBFlushInterval)
	setRuntimeLastSeenAt(t, runtimeID, stale)
	rt := loadRuntime(t, runtimeID)

	recordTestHeartbeat(t, rt)

	_, lastSeen := readRuntimeRow(t, runtimeID)
	if !lastSeen.After(stale.Add(time.Minute)) {
		t.Fatalf("DB last_seen_at should have been flushed: stale=%s after=%s", stale, lastSeen)
	}
}

func TestRecordHeartbeat_OfflineToOnlineForcesDBWrite(t *testing.T) {
	requireHandlerDatabase(t)
	runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)

	fake := &fakeLivenessStore{available: true, aliveOK: true}
	useLivenessStore(t, fake)

	setRuntimeOffline(t, runtimeID)
	setRuntimeLastSeenAt(t, runtimeID, time.Now())
	rt := loadRuntime(t, runtimeID)
	if rt.Status != "offline" {
		t.Fatalf("setup: status = %q, want offline", rt.Status)
	}

	recordTestHeartbeat(t, rt)

	status, _ := readRuntimeRow(t, runtimeID)
	if status != "online" {
		t.Fatalf("expected status=online after offline→online heartbeat, got %q", status)
	}
}

func TestRecordHeartbeat_SweeperRaceRecoversOnline(t *testing.T) {
	requireHandlerDatabase(t)
	runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
	useLivenessStore(t, NewNoopLivenessStore())

	rt := loadRuntime(t, runtimeID)
	if rt.Status != "online" {
		t.Fatalf("setup: runtime should be online, got %q", rt.Status)
	}

	setRuntimeOffline(t, runtimeID)
	recordTestHeartbeat(t, rt)

	status, lastSeen := readRuntimeRow(t, runtimeID)
	if status != "online" {
		t.Fatalf("expected sweeper-raced runtime to recover online, got %q", status)
	}
	if time.Since(lastSeen) > 30*time.Second {
		t.Fatalf("last_seen_at not refreshed: %s ago", time.Since(lastSeen))
	}
}
