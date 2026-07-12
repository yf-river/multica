package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestQuickCreateIssueReplaysQueuedTask(t *testing.T) {
	ctx := context.Background()
	agentID := enableQuickCreateRuntime(t, ctx)
	key := uuid.NewString()
	prompt := "Quick create replay " + uuid.NewString()
	body := map[string]any{
		"agent_id": agentID,
		"prompt":   prompt,
	}

	call := func() (*httptest.ResponseRecorder, QuickCreateIssueResponse) {
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPost, "/api/issues/quick-create", body)
		req.Header.Set("Idempotency-Key", key)
		testHandler.QuickCreateIssue(w, req)
		var response QuickCreateIssueResponse
		if w.Code == http.StatusAccepted {
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("decode quick-create response: %v", err)
			}
		}
		return w, response
	}

	first, created := call()
	if first.Code != http.StatusAccepted {
		t.Fatalf("first quick-create: expected 202, got %d: %s", first.Code, first.Body.String())
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, created.TaskID)
		mustExec(t, context.Background(), `DELETE FROM resource_create_request WHERE resource_type = 'quick_create' AND idempotency_key = $1`, key)
	})

	replayed, replayBody := call()
	if replayed.Code != http.StatusAccepted {
		t.Fatalf("replay: expected 202, got %d: %s", replayed.Code, replayed.Body.String())
	}
	if replayBody.TaskID != created.TaskID || created.TaskID != key {
		t.Fatalf("quick-create identity diverged: key=%s first=%s replay=%s", key, created.TaskID, replayBody.TaskID)
	}
	conflict := httptest.NewRecorder()
	conflictRequest := newRequest(http.MethodPost, "/api/issues/quick-create", map[string]any{
		"agent_id": agentID,
		"prompt":   prompt + " changed",
	})
	conflictRequest.Header.Set("Idempotency-Key", key)
	testHandler.QuickCreateIssue(conflict, conflictRequest)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("same key with changed request: expected 409, got %d: %s", conflict.Code, conflict.Body.String())
	}

	var count int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE context->>'type' = 'quick_create' AND context->>'prompt' = $1
	`, prompt).Scan(&count); err != nil {
		t.Fatalf("count quick-create tasks: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one queued task, got %d", count)
	}
}

func TestQuickCreateIssueConcurrentSameKeyConverges(t *testing.T) {
	ctx := context.Background()
	agentID := enableQuickCreateRuntime(t, ctx)
	key := uuid.NewString()
	prompt := "Quick create concurrent " + uuid.NewString()
	body := map[string]any{"agent_id": agentID, "prompt": prompt}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, key)
		mustExec(t, context.Background(), `DELETE FROM resource_create_request WHERE resource_type = 'quick_create' AND idempotency_key = $1`, key)
	})

	const callers = 8
	type result struct {
		code int
		id   string
		body string
	}
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			req := newRequest(http.MethodPost, "/api/issues/quick-create", body)
			req.Header.Set("Idempotency-Key", key)
			testHandler.QuickCreateIssue(w, req)
			var response QuickCreateIssueResponse
			if w.Code == http.StatusAccepted {
				_ = json.NewDecoder(w.Body).Decode(&response)
			}
			results <- result{code: w.Code, id: response.TaskID, body: w.Body.String()}
		}()
	}
	wg.Wait()
	close(results)

	for got := range results {
		if got.code != http.StatusAccepted || got.id != key {
			t.Fatalf("concurrent quick-create diverged: code=%d id=%s body=%s", got.code, got.id, got.body)
		}
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE id = $1`, key).Scan(&count); err != nil {
		t.Fatalf("count quick-create tasks: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one quick-create task, got %d", count)
	}
}
