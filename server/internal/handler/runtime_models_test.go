package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

func TestModelListStore_RunningRequestTimesOut(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryModelListStore()
	req, err := store.Create(ctx, "runtime-xyz", randomID())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, err := store.PopPending(ctx, "runtime-xyz")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected PopPending to claim the pending request")
	}
	if claimed.Status != runtimeAsyncRunning {
		t.Fatalf("expected Running after PopPending, got %s", claimed.Status)
	}
	if claimed.RunStartedAt == nil {
		t.Fatal("expected RunStartedAt to be set on PopPending")
	}

	aged := time.Now().Add(-(runtimeAsyncRunningTimeout + time.Second))
	claimed.RunStartedAt = &aged
	got, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected stored request")
	}
	if got.Status != runtimeAsyncTimeout {
		t.Fatalf("expected Timeout after running threshold, got %s", got.Status)
	}
	if got.Error == "" {
		t.Fatal("expected timeout error message")
	}
}

func TestModelDefaultJSONContract(t *testing.T) {
	var model agent.Model
	if err := json.Unmarshal([]byte(`{"id":"a","label":"A","default":true}`), &model); err != nil {
		t.Fatalf("decode model: %v", err)
	}
	if !model.Default {
		t.Fatal("default flag lost while decoding")
	}
	raw, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("encode model: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"default":true`)) {
		t.Fatalf("default flag lost while encoding: %s", raw)
	}
}

func TestGetModelListRequestRejectsCrossWorkspaceRequest(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized")
	}

	ctx := context.Background()
	workspaceSlug := fmt.Sprintf("model-access-isolation-%d", time.Now().UnixNano())
	var workspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Model Access Isolation', $1, '', 'MAI')
		RETURNING id
	`, workspaceSlug).Scan(&workspaceID); err != nil {
		t.Fatalf("create isolated workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, scope, last_seen_at
		)
		VALUES ($1, 'model-access-daemon', 'Isolated Runtime', 'cloud',
			'model-access-test', 'online', '', '{}'::jsonb, 'workspace', now())
		RETURNING id
	`, workspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("create isolated runtime: %v", err)
	}

	store := NewInMemoryModelListStore()
	request, err := store.Create(ctx, runtimeID, randomID())
	if err != nil {
		t.Fatalf("create model request: %v", err)
	}
	h := *testHandler
	h.ModelListStore = store

	w := httptest.NewRecorder()
	r := withURLParams(
		newRequest(http.MethodGet, "/api/runtimes/"+runtimeID+"/models/"+request.ID, nil),
		"runtimeId", runtimeID,
		"requestId", request.ID,
	)
	h.GetModelListRequest(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace model request status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestInMemoryModelListStore_HasPending(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryModelListStore()

	if has, err := store.HasPending(ctx, "rt-1"); err != nil || has {
		t.Fatalf("empty store should not report pending: has=%v err=%v", has, err)
	}

	if _, err := store.Create(ctx, "rt-1", randomID()); err != nil {
		t.Fatalf("create: %v", err)
	}
	if has, err := store.HasPending(ctx, "rt-1"); err != nil || !has {
		t.Fatalf("expected pending=true after Create: has=%v err=%v", has, err)
	}
	if has, err := store.HasPending(ctx, "rt-2"); err != nil || has {
		t.Fatalf("expected pending=false for unrelated runtime: has=%v err=%v", has, err)
	}

	if _, err := store.PopPending(ctx, "rt-1"); err != nil {
		t.Fatalf("pop: %v", err)
	}
	if has, err := store.HasPending(ctx, "rt-1"); err != nil || has {
		t.Fatalf("expected pending=false after PopPending: has=%v err=%v", has, err)
	}
}

func TestInMemoryModelListStore_PopPendingPicksOldest(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryModelListStore()

	first, _ := store.Create(ctx, "rt-1", randomID())
	time.Sleep(2 * time.Millisecond)
	second, _ := store.Create(ctx, "rt-1", randomID())

	got, err := store.PopPending(ctx, "rt-1")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if got == nil || got.ID != first.ID {
		t.Fatalf("expected first request, got %+v (second was %s)", got, second.ID)
	}
}
