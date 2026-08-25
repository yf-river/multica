package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func requestThinkingAgentCreate(t *testing.T, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	response := httptest.NewRecorder()
	testHandler.CreateAgent(response, newRequest(http.MethodPost, "/api/agents", body))
	return response, decodeThinkingAgentResponse(t, response)
}

func requestThinkingAgentUpdate(t *testing.T, agentID string, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	response := httptest.NewRecorder()
	request := withURLParam(newRequest(http.MethodPatch, "/api/agents/"+agentID, body), "id", agentID)
	testHandler.UpdateAgent(response, request)
	return response, decodeThinkingAgentResponse(t, response)
}

func decodeThinkingAgentResponse(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if response.Code < http.StatusOK || response.Code >= http.StatusMultipleChoices {
		return nil
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode Agent response: %v", err)
	}
	return body
}

func TestUpdateAgentRollsBackPrimaryFieldsWhenThinkingClearFails(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	runtimeID := createProviderRuntime(t, "claude")
	agentID := createAgentOnRuntime(t, "atomic-agent-update-original", runtimeID, "high", "")
	const functionName = "test_fail_atomic_agent_thinking_clear"
	const triggerName = "test_fail_atomic_agent_thinking_clear_trigger"
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.id = %s::uuid AND NEW.thinking_level IS NULL THEN
				RAISE EXCEPTION 'injected thinking clear failure';
			END IF;
			RETURN NEW;
		END $$;
		DROP TRIGGER IF EXISTS %s ON agent;
		CREATE TRIGGER %s BEFORE UPDATE ON agent FOR EACH ROW EXECUTE FUNCTION %s()
	`, functionName, quoteSQLLiteral(agentID), triggerName, triggerName, functionName)); err != nil {
		t.Fatalf("install thinking clear fault: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON agent; DROP FUNCTION IF EXISTS %s()`, triggerName, functionName))
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id=$1`, agentID)
	})

	w, _ := requestThinkingAgentUpdate(t, agentID, map[string]any{
		"name": "atomic-agent-update-changed", "thinking_level": "",
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("update agent: expected 500, got %d: %s", w.Code, w.Body.String())
	}
	var name, thinking string
	if err := testPool.QueryRow(ctx, `SELECT name, thinking_level FROM agent WHERE id=$1`, agentID).Scan(&name, &thinking); err != nil {
		t.Fatalf("read agent after failed update: %v", err)
	}
	if name != "atomic-agent-update-original" || thinking != "high" {
		t.Fatalf("failed update left name=%q thinking=%q", name, thinking)
	}
}

func TestCreateAgent_ThinkingLevel_ValidationConsistency(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	claudeRuntimeID := createProviderRuntime(t, "claude")

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx,
			`DELETE FROM agent WHERE workspace_id = $1 AND name LIKE 'thinking-test-%'`,
			testWorkspaceID,
		)
	})

	tests := []struct {
		name          string
		thinkingLevel string
		wantStatus    int
		wantLevel     string
	}{
		{name: "empty value succeeds", wantStatus: http.StatusCreated},
		{name: "known claude value succeeds", thinkingLevel: "high", wantStatus: http.StatusCreated, wantLevel: "high"},
		{name: "codex-only token rejected for claude runtime", thinkingLevel: "none", wantStatus: http.StatusBadRequest},
		{name: "garbage value rejected", thinkingLevel: "supersonic", wantStatus: http.StatusBadRequest},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, body := requestThinkingAgentCreate(t, map[string]any{
				"name":                 fmt.Sprintf("thinking-test-%d", index),
				"runtime_id":           claudeRuntimeID,
				"scope":                "personal",
				"max_concurrent_tasks": 1,
				"thinking_level":       test.thinkingLevel,
			})
			if response.Code != test.wantStatus {
				t.Fatalf("expected %d, got %d: %s", test.wantStatus, response.Code, response.Body.String())
			}
			if test.wantLevel != "" && body["thinking_level"] != test.wantLevel {
				t.Errorf("thinking_level = %v, want %q", body["thinking_level"], test.wantLevel)
			}
		})
	}
}

func TestUpdateAgent_ThinkingLevel_TriState(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	claudeRuntimeID := createProviderRuntime(t, "claude")
	agentID := createAgentOnRuntime(t, "thinking-update-test", claudeRuntimeID, "high", "")

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, agentID)
	})

	tests := []struct {
		name       string
		request    map[string]any
		wantStatus int
		wantLevel  string
		checkLevel bool
	}{
		{name: "omitted field leaves value alone", request: map[string]any{"name": "thinking-update-test-renamed"}, wantStatus: http.StatusOK, wantLevel: "high", checkLevel: true},
		{name: "empty string clears", request: map[string]any{"thinking_level": ""}, wantStatus: http.StatusOK, checkLevel: true},
		{name: "garbage value is rejected", request: map[string]any{"thinking_level": "warp-speed"}, wantStatus: http.StatusBadRequest},
		{name: "codex token on claude runtime is rejected", request: map[string]any{"thinking_level": "minimal"}, wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, body := requestThinkingAgentUpdate(t, agentID, test.request)
			if response.Code != test.wantStatus {
				t.Fatalf("expected %d, got %d: %s", test.wantStatus, response.Code, response.Body.String())
			}
			if test.checkLevel && body["thinking_level"] != test.wantLevel {
				t.Errorf("thinking_level = %v, want %q", body["thinking_level"], test.wantLevel)
			}
		})
	}
}

func TestUpdateAgent_RuntimeSwitch_PreservesValidValueRejectsInvalid(t *testing.T) {
	claudeRuntimeID, codexRuntimeID := setupRuntimeSwitchTest(t, "runtime-switch-%")

	tests := []struct {
		name          string
		existingLevel string
		requestLevel  string
		setLevel      bool
		wantStatus    int
		wantLevel     string
	}{
		{name: "existing valid value is kept", existingLevel: "high", wantStatus: http.StatusOK, wantLevel: "high"},
		{name: "existing invalid value is rejected", existingLevel: "max", wantStatus: http.StatusBadRequest},
		{name: "explicit clear permits switch", existingLevel: "max", setLevel: true, wantStatus: http.StatusOK},
		{name: "explicit valid replacement permits switch", existingLevel: "max", requestLevel: "minimal", setLevel: true, wantStatus: http.StatusOK, wantLevel: "minimal"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agentID := createAgentOnRuntime(t, fmt.Sprintf("runtime-switch-%d", index), claudeRuntimeID, test.existingLevel, "")
			request := map[string]any{"runtime_id": codexRuntimeID}
			if test.setLevel {
				request["thinking_level"] = test.requestLevel
			}
			response, body := requestThinkingAgentUpdate(t, agentID, request)
			if response.Code != test.wantStatus {
				t.Fatalf("expected %d, got %d: %s", test.wantStatus, response.Code, response.Body.String())
			}
			if test.wantStatus == http.StatusOK && body["thinking_level"] != test.wantLevel {
				t.Errorf("thinking_level = %v, want %q", body["thinking_level"], test.wantLevel)
			}
		})
	}
}

func TestUpdateAgent_RuntimeSwitch_ClearsKnownIncompatibleModel(t *testing.T) {
	claudeRuntimeID, codexRuntimeID := setupRuntimeSwitchTest(t, "runtime-model-switch-%")

	tests := []struct {
		name          string
		existingModel string
		requestModel  string
		setModel      bool
		wantModel     string
	}{
		{name: "clears known foreign model", existingModel: "claude-sonnet-4-6"},
		{name: "clears unsupported provider-prefixed model", existingModel: "openai/gpt-4o"},
		{name: "keeps exact target model", existingModel: "gpt-5.5", wantModel: "gpt-5.5"},
		{name: "explicit replacement wins", existingModel: "claude-sonnet-4-6", requestModel: "gpt-5.5", setModel: true, wantModel: "gpt-5.5"},
		{name: "keeps unknown custom model", existingModel: "private-lab-model", wantModel: "private-lab-model"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agentID := createAgentOnRuntime(t, fmt.Sprintf("runtime-model-switch-%d", index), claudeRuntimeID, "", test.existingModel)
			request := map[string]any{"runtime_id": codexRuntimeID}
			if test.setModel {
				request["model"] = test.requestModel
			}
			response, body := requestThinkingAgentUpdate(t, agentID, request)
			if response.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
			}
			if body["model"] != test.wantModel {
				t.Errorf("model = %v, want %q", body["model"], test.wantModel)
			}
		})
	}
}

func setupRuntimeSwitchTest(t *testing.T, agentNamePattern string) (string, string) {
	t.Helper()
	requireHandlerDatabase(t)
	claudeRuntimeID := createProviderRuntime(t, "claude")
	codexRuntimeID := createProviderRuntime(t, "codex")
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE workspace_id = $1 AND name LIKE $2`, testWorkspaceID, agentNamePattern)
	})
	return claudeRuntimeID, codexRuntimeID
}

func createProviderRuntime(t *testing.T, provider string) string {
	t.Helper()
	var runtimeID string
	err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, last_seen_at, owner_id, scope
		)
		VALUES ($1, NULL, $2, 'cloud', $3, 'online', $4, '{}'::jsonb, now(), $5, 'personal')
		RETURNING id
	`, testWorkspaceID, provider+" Thinking Runtime", provider, provider+" thinking-level test runtime", testUserID).Scan(&runtimeID)
	if err != nil {
		t.Fatalf("create %s runtime: %v", provider, err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})
	return runtimeID
}

func createAgentOnRuntime(t *testing.T, name, runtimeID, thinkingLevel, model string) string {
	t.Helper()
	var agentID string
	err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, scope, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args, thinking_level, model
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'personal', 1, $4, '', '{}'::jsonb, '[]'::jsonb, NULLIF($5, ''), NULLIF($6, ''))
		RETURNING id
	`, testWorkspaceID, name, runtimeID, testUserID, thinkingLevel, model).Scan(&agentID)
	if err != nil {
		t.Fatalf("create agent on runtime %s: %v", runtimeID, err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
	})
	return agentID
}
