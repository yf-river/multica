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

func TestCreateAgentFromTemplate_ReplaysCommittedResponse(t *testing.T) {
	name := "Template replay " + uuid.NewString()
	key := uuid.NewString()
	body := map[string]any{
		"template_slug": "brainstormer",
		"name":          name,
		"runtime_id":    testRuntimeID,
		"scope":         "personal",
	}

	call := func() (*httptest.ResponseRecorder, CreateAgentFromTemplateResponse) {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPost, "/api/agents/from-template", body)
		req.Header.Set("Idempotency-Key", key)
		testHandler.CreateAgentFromTemplate(w, req)
		var response CreateAgentFromTemplateResponse
		if w.Code == http.StatusCreated {
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("decode create response: %v", err)
			}
		}
		return w, response
	}

	first, created := call()
	if first.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d: %s", first.Code, first.Body.String())
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent WHERE id = $1`, created.Agent.ID)
		mustExec(t, context.Background(), `
			DELETE FROM resource_create_request
			WHERE workspace_id = $1 AND actor_id = $2 AND resource_type = 'agent'
			  AND idempotency_key = $3
		`, testWorkspaceID, testUserID, key)
	})

	replayed, replayBody := call()
	if replayed.Code != http.StatusCreated {
		t.Fatalf("replay: expected 201, got %d: %s", replayed.Code, replayed.Body.String())
	}
	if replayBody.Agent.ID != created.Agent.ID {
		t.Fatalf("replay created a different agent: first=%s replay=%s", created.Agent.ID, replayBody.Agent.ID)
	}
	if replayed.Body.String() != first.Body.String() {
		t.Fatalf("replay response differs:\nfirst:  %s\nreplay: %s", first.Body.String(), replayed.Body.String())
	}

	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2
	`, testWorkspaceID, name).Scan(&count); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one durable agent, got %d", count)
	}
}

func TestCreateAgentFromTemplate_RejectsMalformedExtraSkillID(t *testing.T) {
	name := "Template invalid skill " + uuid.NewString()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/agents/from-template", map[string]any{
		"template_slug":   "brainstormer",
		"name":            name,
		"runtime_id":      testRuntimeID,
		"scope":           "personal",
		"extra_skill_ids": []string{"not-a-uuid"},
	})
	testHandler.CreateAgentFromTemplate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2
	`, testWorkspaceID, name).Scan(&count); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if count != 0 {
		t.Fatalf("invalid request created %d agents", count)
	}
}

func TestCreateAgentFromTemplate_ConcurrentSameKeyConverges(t *testing.T) {
	const callers = 8
	name := "Template concurrent " + uuid.NewString()
	key := uuid.NewString()
	body := map[string]any{
		"template_slug": "brainstormer",
		"name":          name,
		"runtime_id":    testRuntimeID,
		"scope":         "personal",
	}
	type result struct {
		code     int
		response CreateAgentFromTemplateResponse
		body     string
	}
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			req := newRequest(http.MethodPost, "/api/agents/from-template", body)
			req.Header.Set("Idempotency-Key", key)
			testHandler.CreateAgentFromTemplate(w, req)
			var response CreateAgentFromTemplateResponse
			if w.Code == http.StatusCreated {
				_ = json.NewDecoder(w.Body).Decode(&response)
			}
			results <- result{code: w.Code, response: response, body: w.Body.String()}
		}()
	}
	wg.Wait()
	close(results)

	var agentID string
	for got := range results {
		if got.code != http.StatusCreated {
			t.Fatalf("concurrent create: expected 201, got %d: %s", got.code, got.body)
		}
		if got.response.Agent.ID == "" {
			t.Fatalf("concurrent create returned no agent: %s", got.body)
		}
		if agentID == "" {
			agentID = got.response.Agent.ID
		} else if got.response.Agent.ID != agentID {
			t.Fatalf("concurrent calls diverged: first=%s got=%s", agentID, got.response.Agent.ID)
		}
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
		mustExec(t, context.Background(), `
			DELETE FROM resource_create_request
			WHERE workspace_id = $1 AND actor_id = $2 AND resource_type = 'agent'
			  AND idempotency_key = $3
		`, testWorkspaceID, testUserID, key)
	})

	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM agent WHERE workspace_id = $1 AND name = $2
	`, testWorkspaceID, name).Scan(&count); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one durable agent, got %d", count)
	}
}
