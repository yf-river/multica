package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func createAgentWithKey(t *testing.T, key string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/agents?workspace_id="+testWorkspaceID, body)
	req.Header.Set("Idempotency-Key", key)
	testHandler.CreateAgent(w, req)
	return w
}

func cleanupAgentCreateRequest(t *testing.T, key, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name)
		_, _ = testPool.Exec(ctx, `DELETE FROM resource_create_request WHERE workspace_id = $1 AND resource_type = 'agent' AND idempotency_key = $2`, testWorkspaceID, key)
	})
}

func TestCreateAgent_IdempotentReplayConflictAndConcurrentCreate(t *testing.T) {
	key := uuid.NewString()
	name := "idempotent agent " + uuid.NewString()
	cleanupAgentCreateRequest(t, key, name)
	body := map[string]any{
		"name": name, "runtime_id": testRuntimeID, "scope": "personal",
		"instructions": "Use the current contract",
	}

	const callers = 8
	responses := make(chan *httptest.ResponseRecorder, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- createAgentWithKey(t, key, body)
		}()
	}
	wg.Wait()
	close(responses)

	ids := map[string]struct{}{}
	var firstBody string
	for response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("create = %d %s", response.Code, response.Body.String())
		}
		if firstBody == "" {
			firstBody = response.Body.String()
		} else if response.Body.String() != firstBody {
			t.Fatalf("replay body differs\nfirst: %s\nnext: %s", firstBody, response.Body.String())
		}
		var agent AgentResponse
		if err := json.Unmarshal(response.Body.Bytes(), &agent); err != nil {
			t.Fatal(err)
		}
		ids[agent.ID] = struct{}{}
	}
	if len(ids) != 1 {
		t.Fatalf("responses returned %d agent ids: %v", len(ids), ids)
	}

	conflict := createAgentWithKey(t, key, map[string]any{
		"name": name + " changed", "runtime_id": testRuntimeID, "scope": "personal",
	})
	if conflict.Code != http.StatusConflict || conflict.Body.String() != "{\"code\":\"idempotency_conflict\",\"error\":\"Idempotency-Key was already used with a different request\"}\n" {
		t.Fatalf("conflict = %d %s", conflict.Code, conflict.Body.String())
	}
	var agents, requests int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name).Scan(&agents)
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM resource_create_request WHERE workspace_id = $1 AND resource_type = 'agent' AND idempotency_key = $2`, testWorkspaceID, key).Scan(&requests)
	if agents != 1 || requests != 1 {
		t.Fatalf("agents=%d requests=%d, want 1/1", agents, requests)
	}
}

func TestCreateAgent_ResponseCompletionFailureRollsBackAgent(t *testing.T) {
	key := uuid.NewString()
	name := "failed agent " + uuid.NewString()
	cleanupAgentCreateRequest(t, key, name)
	suffix := uuid.NewString()
	functionName := quoteIdentifier("fail_agent_create_completion_" + suffix)
	triggerName := quoteIdentifier("fail_agent_create_completion_trigger_" + suffix)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.resource_type = 'agent' AND NEW.idempotency_key = %s::uuid AND NEW.response_body IS NOT NULL THEN
				RAISE EXCEPTION 'forced agent request completion failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON resource_create_request
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, quoteSQLLiteral(key), triggerName, functionName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON resource_create_request`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})

	response := createAgentWithKey(t, key, map[string]any{
		"name": name, "runtime_id": testRuntimeID, "scope": "personal",
	})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response = %d %s, want 500", response.Code, response.Body.String())
	}
	var agents, requests int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name).Scan(&agents)
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM resource_create_request WHERE workspace_id = $1 AND resource_type = 'agent' AND idempotency_key = $2`, testWorkspaceID, key).Scan(&requests)
	if agents != 0 || requests != 0 {
		t.Fatalf("failed create left agents=%d requests=%d", agents, requests)
	}
}
