package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestCreateAutopilotRequiresInitialTriggerAndIdempotencyKey(t *testing.T) {
	h := &Handler{}

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/autopilots", map[string]any{
		"title":          "Missing trigger",
		"assignee_type":  "agent",
		"assignee_id":    "10000000-0000-4000-8000-000000000001",
		"execution_mode": "run_only",
	})
	h.CreateAutopilot(w, req)
	if w.Code != http.StatusBadRequest || w.Body.String() != "{\"error\":\"kind is required\"}\n" {
		t.Fatalf("missing trigger response = %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = newRequest(http.MethodPost, "/api/autopilots", currentAutopilotCreateBody(map[string]any{
		"title":          "Missing key",
		"assignee_id":    "10000000-0000-4000-8000-000000000001",
		"execution_mode": "run_only",
	}))
	h.CreateAutopilot(w, req)
	if w.Code != http.StatusBadRequest || w.Body.String() != "{\"code\":\"idempotency_key_required\",\"error\":\"Idempotency-Key header is required\"}\n" {
		t.Fatalf("missing key response = %d %s", w.Code, w.Body.String())
	}
}

func TestCreateAutopilotConcurrentReplayCommitsOneAutopilotAndTrigger(t *testing.T) {
	ctx := context.Background()
	agentID := firstWorkspaceAgentID(t, ctx)
	title := fmt.Sprintf("Atomic autopilot replay %d", time.Now().UnixNano())
	const key = "20000000-0000-4000-8000-000000000001"
	body := currentAutopilotCreateBody(map[string]any{
		"title":          title,
		"assignee_id":    agentID,
		"execution_mode": "run_only",
	})
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title)
	})

	type result struct {
		status int
		body   string
		value  AutopilotResponse
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			w := httptest.NewRecorder()
			req := newRequest(http.MethodPost, "/api/autopilots?workspace_id="+testWorkspaceID, body)
			req.Header.Set("Idempotency-Key", key)
			testHandler.CreateAutopilot(w, req)
			var response AutopilotResponse
			_ = json.Unmarshal(w.Body.Bytes(), &response)
			results <- result{status: w.Code, body: w.Body.String(), value: response}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	responses := make([]result, 0, 2)
	for response := range results {
		responses = append(responses, response)
	}
	for _, response := range responses {
		if response.status != http.StatusCreated || response.value.InitialTrigger == nil {
			t.Fatalf("create response = %d %s", response.status, response.body)
		}
	}
	if responses[0].value.ID != responses[1].value.ID ||
		responses[0].value.InitialTrigger.ID != responses[1].value.InitialTrigger.ID {
		t.Fatalf("concurrent responses differ: %+v vs %+v", responses[0].value, responses[1].value)
	}

	var autopilotCount, triggerCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM autopilot WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title).Scan(&autopilotCount); err != nil {
		t.Fatalf("count autopilots: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM autopilot_trigger WHERE autopilot_id = $1`, responses[0].value.ID).Scan(&triggerCount); err != nil {
		t.Fatalf("count triggers: %v", err)
	}
	if autopilotCount != 1 || triggerCount != 1 {
		t.Fatalf("committed autopilots/triggers = %d/%d, want 1/1", autopilotCount, triggerCount)
	}
	var linkedTriggerID string
	if err := testPool.QueryRow(ctx, `SELECT initial_trigger_id FROM autopilot WHERE id = $1`, responses[0].value.ID).Scan(&linkedTriggerID); err != nil {
		t.Fatalf("load initial trigger link: %v", err)
	}
	if linkedTriggerID != responses[0].value.InitialTrigger.ID {
		t.Fatalf("initial_trigger_id = %s, response = %s", linkedTriggerID, responses[0].value.InitialTrigger.ID)
	}

	conflictWriter := httptest.NewRecorder()
	conflictBody := currentAutopilotCreateBody(map[string]any{
		"title":          title + " changed",
		"assignee_id":    agentID,
		"execution_mode": "run_only",
	})
	conflictRequest := newRequest(http.MethodPost, "/api/autopilots?workspace_id="+testWorkspaceID, conflictBody)
	conflictRequest.Header.Set("Idempotency-Key", key)
	testHandler.CreateAutopilot(conflictWriter, conflictRequest)
	if conflictWriter.Code != http.StatusConflict {
		t.Fatalf("changed replay status = %d, want 409: %s", conflictWriter.Code, conflictWriter.Body.String())
	}
}

func TestCreateAutopilotRollsBackWhenInitialTriggerInsertFails(t *testing.T) {
	ctx := context.Background()
	agentID := firstWorkspaceAgentID(t, ctx)
	title := fmt.Sprintf("Atomic trigger rollback %d", time.Now().UnixNano())
	installAutopilotTriggerInsertFailure(t)

	w := httptest.NewRecorder()
	req := newAutopilotCreateRequest("/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{
		"title":          title,
		"assignee_id":    agentID,
		"execution_mode": "run_only",
	})
	testHandler.CreateAutopilot(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}

	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM autopilot WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title).Scan(&count); err != nil {
		t.Fatalf("count autopilots: %v", err)
	}
	if count != 0 {
		t.Fatalf("autopilot rows after trigger failure = %d, want 0", count)
	}
}

func firstWorkspaceAgentID(t *testing.T, ctx context.Context) string {
	t.Helper()
	var agentID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load test agent: %v", err)
	}
	return agentID
}

func installAutopilotTriggerInsertFailure(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	functionName := fmt.Sprintf("autopilot_trigger_fail_fn_%d", suffix)
	triggerName := fmt.Sprintf("autopilot_trigger_fail_%d", suffix)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON autopilot_trigger`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'forced autopilot trigger insert failure';
END;
$$;
CREATE TRIGGER %s BEFORE INSERT ON autopilot_trigger
FOR EACH ROW EXECUTE FUNCTION %s();
`, functionName, triggerName, functionName)); err != nil {
		t.Fatalf("install trigger failure: %v", err)
	}
}
