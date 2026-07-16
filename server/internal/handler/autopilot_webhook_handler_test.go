package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// All tests in this file require a working DB. testHandler / testWorkspaceID /
// testUserID / testRuntimeID are wired in TestMain (handler_test.go) and
// TestMain skips the suite if Postgres isn't reachable.

// ── Fixture helpers ─────────────────────────────────────────────────────────

func createWebhookTestAgent(t *testing.T, name string) string {
	t.Helper()
	var agentID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, scope, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args, mcp_config
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'personal', 1, $4, '', '{}'::jsonb, '[]'::jsonb, '{}'::jsonb)
		RETURNING id
	`, testWorkspaceID, name, testRuntimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
	})
	return agentID
}

func createWebhookTestAutopilot(t *testing.T, status string) string {
	t.Helper()
	return createWebhookTestAutopilotForAgent(t, createWebhookTestAgent(t, "Webhook test "+uuid.NewString()), status)
}

func createWebhookTestAutopilotForAgent(t *testing.T, agentID, status string) string {
	t.Helper()
	var apID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO autopilot (
			workspace_id, title, assignee_id, status, execution_mode,
			created_by_type, created_by_id
		) VALUES ($1, $2, $3, $4, $5, 'member', $6)
		RETURNING id
	`, testWorkspaceID, "Webhook test "+status, agentID, status, "run_only", testUserID).Scan(&apID); err != nil {
		t.Fatalf("create autopilot: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, apID)
	})
	return apID
}

func createWebhookTrigger(t *testing.T, autopilotID string, filters ...WebhookEventFilter) AutopilotTriggerResponse {
	t.Helper()
	body := map[string]any{"kind": "webhook"}
	if len(filters) > 0 {
		body["event_filters"] = filters
	}
	w := requestCreateAutopilotTrigger(t, autopilotID, body, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateAutopilotTrigger: expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	var resp AutopilotTriggerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func requestCreateAutopilotTrigger(t *testing.T, autopilotID string, body any, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest("POST", "/api/autopilots/"+autopilotID+"/triggers", body)
	req = withURLParams(req, "id", autopilotID)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	w := httptest.NewRecorder()
	testHandler.CreateAutopilotTrigger(w, req)
	return w
}

func requestUpdateAutopilotTrigger(t *testing.T, autopilotID, triggerID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest("PATCH", "/api/autopilots/"+autopilotID+"/triggers/"+triggerID, body)
	req = withURLParams(req, "id", autopilotID, "triggerId", triggerID)
	w := httptest.NewRecorder()
	testHandler.UpdateAutopilotTrigger(w, req)
	return w
}

func requestRotateWebhookToken(t *testing.T, autopilotID, triggerID, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest("POST", fmt.Sprintf("/api/autopilots/%s/triggers/%s/rotate-webhook-token", autopilotID, triggerID), nil)
	req = withURLParams(req, "id", autopilotID, "triggerId", triggerID)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	w := httptest.NewRecorder()
	testHandler.RotateAutopilotTriggerWebhookToken(w, req)
	return w
}

func decodeAutopilotTriggerResponse(t *testing.T, w *httptest.ResponseRecorder) AutopilotTriggerResponse {
	t.Helper()
	var response AutopilotTriggerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeWebhookResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode webhook response: %v", err)
	}
	return response
}

func TestCreateAutopilotTrigger_ReplaysCommittedCreate(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	requestKey := uuid.NewString()
	create := func() AutopilotTriggerResponse {
		t.Helper()
		w := requestCreateAutopilotTrigger(t, apID, map[string]any{"kind": "webhook"}, requestKey)
		if w.Code != http.StatusCreated {
			t.Fatalf("create trigger status = %d: %s", w.Code, w.Body.String())
		}
		return decodeAutopilotTriggerResponse(t, w)
	}

	first := create()
	replay := create()
	if !reflect.DeepEqual(replay, first) {
		t.Fatalf("replayed trigger create = %#v, want exact %#v", replay, first)
	}
	conflict := requestCreateAutopilotTrigger(t, apID, map[string]any{
		"kind": "schedule", "cron_expression": "0 9 * * *", "timezone": "UTC",
	}, requestKey)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed trigger create with same key = %d %s, want 409", conflict.Code, conflict.Body.String())
	}
}

func TestCreateAutopilotTrigger_ConcurrentReplayCreatesOnce(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	requestKey := uuid.NewString()

	const workers = 8
	responses := make(chan AutopilotTriggerResponse, workers)
	statuses := make(chan int, workers)
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(workers)
	for range workers {
		go func() {
			defer done.Done()
			start.Wait()
			w := requestCreateAutopilotTrigger(t, apID, map[string]any{"kind": "webhook"}, requestKey)
			response := decodeAutopilotTriggerResponse(t, w)
			statuses <- w.Code
			responses <- response
		}()
	}
	start.Done()
	done.Wait()
	close(statuses)
	close(responses)

	for status := range statuses {
		if status != http.StatusCreated {
			t.Fatalf("concurrent trigger create status = %d, want 201", status)
		}
	}
	var first AutopilotTriggerResponse
	for response := range responses {
		if first.ID == "" {
			first = response
		} else if !reflect.DeepEqual(response, first) {
			t.Fatalf("concurrent trigger create responses differ: %#v and %#v", first, response)
		}
	}
	var triggers, requests int
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM autopilot_trigger WHERE autopilot_id = $1),
			(SELECT count(*) FROM resource_create_request WHERE resource_type = 'autopilot_trigger' AND idempotency_key = $2)
	`, apID, requestKey).Scan(&triggers, &requests); err != nil {
		t.Fatal(err)
	}
	if triggers != 1 || requests != 1 {
		t.Fatalf("concurrent trigger create left triggers=%d requests=%d, want 1/1", triggers, requests)
	}
}

func TestCreateAutopilotTrigger_CompletionFailureRollsBackCreate(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	requestKey := uuid.NewString()
	installResourceCreateCompletionFailure(t, resourceTypeAutopilotTrigger, requestKey)

	w := requestCreateAutopilotTrigger(t, apID, map[string]any{"kind": "webhook"}, requestKey)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("forced trigger create completion failure = %d %s, want 500", w.Code, w.Body.String())
	}
	var triggers, requests int
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM autopilot_trigger WHERE autopilot_id = $1),
			(SELECT count(*) FROM resource_create_request WHERE resource_type = 'autopilot_trigger' AND idempotency_key = $2)
	`, apID, requestKey).Scan(&triggers, &requests); err != nil {
		t.Fatal(err)
	}
	if triggers != 0 || requests != 0 {
		t.Fatalf("failed trigger create left triggers=%d requests=%d, want 0/0", triggers, requests)
	}
}

func TestWebhookHandler_FiltersUndeclaredEvent(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID,
		WebhookEventFilter{Event: "workflow_run", Actions: []string{"completed"}},
		WebhookEventFilter{Event: "check_suite", Actions: []string{"completed"}},
	)

	w := postWebhook(t, *trig.WebhookToken, map[string]any{
		"action":       "in_progress",
		"workflow_run": map[string]any{"id": 123},
	}, map[string]string{"X-GitHub-Event": "workflow_run"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	resp := decodeWebhookResponse(t, w)
	if resp["status"] != "ignored" || resp["reason"] != "event_filtered" {
		t.Fatalf("expected ignored/event_filtered, got %#v body=%s", resp, w.Body.String())
	}
	if _, ok := resp["run_id"]; ok {
		t.Fatalf("filtered response must not include run_id: %#v", resp)
	}

	runs, err := testHandler.Queries.ListAutopilotRuns(context.Background(), db.ListAutopilotRunsParams{
		AutopilotID: parseUUID(apID),
		Limit:       10,
		Offset:      0,
	})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("filtered webhook should not create runs, got %d", len(runs))
	}
}

func TestWebhookHandler_AllowsDeclaredEvent(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID,
		WebhookEventFilter{Event: "workflow_run", Actions: []string{"completed"}},
	)

	w := postWebhook(t, *trig.WebhookToken, map[string]any{
		"action":       "completed",
		"workflow_run": map[string]any{"id": 123},
	}, map[string]string{"X-GitHub-Event": "workflow_run"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	resp := decodeWebhookResponse(t, w)
	if resp["status"] != "accepted" && resp["status"] != "skipped" {
		t.Fatalf("expected accepted or skipped, got %#v", resp)
	}
}

func TestWebhookHandler_EmptyFiltersAllowsAll(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)

	w := postWebhook(t, *trig.WebhookToken, map[string]any{
		"action":       "in_progress",
		"workflow_run": map[string]any{"id": 123},
	}, map[string]string{"X-GitHub-Event": "workflow_run"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	resp := decodeWebhookResponse(t, w)
	if resp["status"] != "accepted" && resp["status"] != "skipped" {
		t.Fatalf("expected accepted or skipped, got %#v", resp)
	}
}

// ── HTTP contract: event_filters JSON shape & PATCH semantics ──────────────
//
// The frontend contract uses JSON arrays and tri-state updates: omitted
// preserves filters, [] clears them, and a populated array replaces them.

func TestCreateWebhookTrigger_EventFiltersRoundTripAsJSONArray(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")

	body := map[string]any{
		"kind": "webhook",
		"event_filters": []map[string]any{
			{"event": "workflow_run", "actions": []string{"completed"}},
			{"event": "pull_request", "actions": []string{"opened", "synchronize"}},
		},
	}
	w := requestCreateAutopilotTrigger(t, apID, body, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}

	var typed AutopilotTriggerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &typed); err != nil {
		t.Fatalf("decode typed: %v", err)
	}
	if len(typed.EventFilters) != 2 {
		t.Fatalf("expected 2 filters, got %d body=%s", len(typed.EventFilters), w.Body.String())
	}
	if typed.EventFilters[0].Event != "workflow_run" ||
		len(typed.EventFilters[0].Actions) != 1 ||
		typed.EventFilters[0].Actions[0] != "completed" {
		t.Fatalf("first filter mismatch: %#v", typed.EventFilters[0])
	}
	if typed.EventFilters[1].Event != "pull_request" || len(typed.EventFilters[1].Actions) != 2 {
		t.Fatalf("second filter mismatch: %#v", typed.EventFilters[1])
	}

	// Confirm the wire value is an array rather than a base64 string.
	var raw struct {
		EventFilters json.RawMessage `json:"event_filters"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	trimmed := bytes.TrimSpace(raw.EventFilters)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		t.Fatalf("event_filters must serialize as a JSON array, got %s", raw.EventFilters)
	}
}

func TestUpdateWebhookTrigger_ExplicitEmptyArrayClearsFilters(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")

	created := createWebhookTrigger(t, apID,
		WebhookEventFilter{Event: "workflow_run", Actions: []string{"completed"}},
	)
	if len(created.EventFilters) != 1 {
		t.Fatalf("seed should have 1 filter, got %d", len(created.EventFilters))
	}

	w := requestUpdateAutopilotTrigger(t, apID, created.ID, map[string]any{
		"event_filters": []any{},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var updated AutopilotTriggerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(updated.EventFilters) != 0 {
		t.Fatalf("expected cleared filters in response, got %#v", updated.EventFilters)
	}

	// Stored row should now accept any event (matcher sees length 0).
	row, err := testHandler.Queries.GetAutopilotTrigger(context.Background(), parseUUID(created.ID))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	env := WebhookEnvelope{
		Event:        "github.something_else.opened",
		EventPayload: json.RawMessage(`{"action":"opened"}`),
	}
	if !webhookEventAllowedByTriggerScope(row.EventFilters, env) {
		t.Fatal("after clear, matcher should allow all events")
	}
}

func TestUpdateWebhookTrigger_OmittedFiltersPreserveExisting(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")

	created := createWebhookTrigger(t, apID,
		WebhookEventFilter{Event: "workflow_run", Actions: []string{"completed"}},
	)

	// PATCH that does NOT include event_filters at all. Must leave the
	// existing filter set untouched (omitted ≠ clear).
	w := requestUpdateAutopilotTrigger(t, apID, created.ID, map[string]any{
		"label": "renamed-but-keep-filters",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var updated AutopilotTriggerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(updated.EventFilters) != 1 || updated.EventFilters[0].Event != "workflow_run" {
		t.Fatalf("filters must be preserved when field omitted, got %#v", updated.EventFilters)
	}
}

func TestUpdateWebhookTrigger_ReplacesFilters(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")

	created := createWebhookTrigger(t, apID,
		WebhookEventFilter{Event: "workflow_run", Actions: []string{"completed"}},
	)

	w := requestUpdateAutopilotTrigger(t, apID, created.ID, map[string]any{
		"event_filters": []map[string]any{
			{"event": "pull_request", "actions": []string{"opened"}},
			{"event": "issues"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var updated AutopilotTriggerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(updated.EventFilters) != 2 {
		t.Fatalf("expected 2 replaced filters, got %d", len(updated.EventFilters))
	}
	if updated.EventFilters[0].Event != "pull_request" || updated.EventFilters[1].Event != "issues" {
		t.Fatalf("replaced filter list wrong: %#v", updated.EventFilters)
	}
}

func TestCreateAutopilotTrigger_RejectsInvalidEventFilter(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")

	w := requestCreateAutopilotTrigger(t, apID, map[string]any{
		"kind": "webhook",
		"event_filters": []map[string]any{
			{"event": "", "actions": []string{"completed"}},
		},
	}, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on empty event name, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestCreateAutopilotTrigger_RejectsEventFiltersOnSchedule(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")

	w := requestCreateAutopilotTrigger(t, apID, map[string]any{
		"kind":            "schedule",
		"cron_expression": "0 9 * * *",
		"event_filters": []map[string]any{
			{"event": "workflow_run"},
		},
	}, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on event_filters for schedule trigger, got %d body=%s", w.Code, w.Body.String())
	}
}

func postWebhook(t *testing.T, token string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	switch v := body.(type) {
	case []byte:
		buf.Write(v)
	case string:
		buf.WriteString(v)
	default:
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req := httptest.NewRequest("POST", "/api/webhooks/autopilots/"+token, &buf)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req = withURLParam(req, "token", token)
	w := httptest.NewRecorder()
	testHandler.HandleAutopilotWebhook(w, req)
	return w
}

// ── Tests ───────────────────────────────────────────────────────────────────

func TestCreateWebhookTrigger_GeneratesToken(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")

	resp := createWebhookTrigger(t, apID)
	if resp.Kind != "webhook" {
		t.Fatalf("kind: %q", resp.Kind)
	}
	if resp.WebhookToken == nil || *resp.WebhookToken == "" {
		t.Fatal("webhook_token should be present and non-empty")
	}
	if !strings.HasPrefix(*resp.WebhookToken, "awt_") {
		t.Fatalf("token prefix: %q", *resp.WebhookToken)
	}
	if resp.WebhookPath == nil {
		t.Fatal("webhook_path should be present")
	}
	if !strings.HasSuffix(*resp.WebhookPath, *resp.WebhookToken) {
		t.Fatalf("webhook_path %q should contain token %q", *resp.WebhookPath, *resp.WebhookToken)
	}
}

func TestCreateWebhookTrigger_TwoUniqueTokens(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")

	a := createWebhookTrigger(t, apID)
	b := createWebhookTrigger(t, apID)
	if a.WebhookToken == nil || b.WebhookToken == nil {
		t.Fatal("missing tokens")
	}
	if *a.WebhookToken == *b.WebhookToken {
		t.Fatalf("tokens should differ: %q == %q", *a.WebhookToken, *b.WebhookToken)
	}
}

func TestCreateWebhookTrigger_PublicURLAffectsResponse(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")

	prev := testHandler.cfg.PublicURL
	t.Cleanup(func() { testHandler.cfg.PublicURL = prev })

	testHandler.cfg.PublicURL = ""
	respNoURL := createWebhookTrigger(t, apID)
	if respNoURL.WebhookURL != nil {
		t.Fatalf("webhook_url should be nil when PublicURL unset, got %q", *respNoURL.WebhookURL)
	}

	testHandler.cfg.PublicURL = "https://app.example"
	respURL := createWebhookTrigger(t, apID)
	if respURL.WebhookURL == nil {
		t.Fatal("webhook_url should be present when PublicURL set")
	}
	if !strings.HasPrefix(*respURL.WebhookURL, "https://app.example/api/webhooks/autopilots/") {
		t.Fatalf("webhook_url shape: %q", *respURL.WebhookURL)
	}
}

func TestWebhookHandler_404OnUnknownToken(t *testing.T) {
	w := postWebhook(t, "awt_unknown_token_value", map[string]any{"hello": "world"}, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestWebhookHandler_RejectsInvalidBodies(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)

	for name, body := range map[string][]byte{
		"empty":   {},
		"invalid": []byte(`not json`),
		"scalar":  []byte(`"hello"`),
	} {
		t.Run(name, func(t *testing.T) {
			w := postWebhook(t, *trig.WebhookToken, body, nil)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestWebhookHandler_RejectsOversized(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)

	big := make([]byte, maxWebhookBodyBytes+10)
	for i := range big {
		big[i] = 'a'
	}
	body := append([]byte(`{"x":"`), big...)
	body = append(body, []byte(`"}`)...)

	w := postWebhook(t, *trig.WebhookToken, body, nil)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestWebhookHandler_DisabledTriggerReturnsIgnored(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)

	if _, err := testHandler.Queries.UpdateAutopilotTrigger(context.Background(), db.UpdateAutopilotTriggerParams{
		ID:      parseUUID(trig.ID),
		Enabled: pgtype.Bool{Bool: false, Valid: true},
	}); err != nil {
		t.Fatalf("disable trigger: %v", err)
	}

	w := postWebhook(t, *trig.WebhookToken, map[string]any{"hello": "world"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	resp := decodeWebhookResponse(t, w)
	if resp["status"] != "ignored" {
		t.Fatalf("status: %v", resp["status"])
	}
	if resp["reason"] != "trigger_disabled" {
		t.Fatalf("reason: %v", resp["reason"])
	}
}

func TestWebhookHandler_PausedAutopilotReturnsIgnored(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "paused")
	trig := createWebhookTrigger(t, apID)

	w := postWebhook(t, *trig.WebhookToken, map[string]any{"x": 1}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	resp := decodeWebhookResponse(t, w)
	if resp["reason"] != "autopilot_paused" {
		t.Fatalf("reason: %v", resp["reason"])
	}
}

func TestWebhookHandler_ActiveDispatchesRunWithPayload(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)

	w := postWebhook(t, *trig.WebhookToken, map[string]any{
		"event":        "demo.received",
		"eventPayload": map[string]any{"k": "v"},
	}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	resp := decodeWebhookResponse(t, w)
	if resp["status"] != "accepted" {
		t.Fatalf("expected accepted, got %v body=%s", resp["status"], w.Body.String())
	}
	runID, _ := resp["run_id"].(string)
	if runID == "" {
		t.Fatal("run_id missing from response")
	}

	// Validate the persisted run carries the normalized envelope.
	run, err := testHandler.Queries.GetAutopilotRun(context.Background(), parseUUID(runID))
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if run.Source != "webhook" {
		t.Fatalf("run.source: %q", run.Source)
	}
	if uuidToString(run.TriggerID) != trig.ID {
		t.Fatalf("run.trigger_id mismatch: %q vs %q", uuidToString(run.TriggerID), trig.ID)
	}
	var payload struct {
		Event        string                 `json:"event"`
		EventPayload map[string]interface{} `json:"eventPayload"`
	}
	if err := json.Unmarshal(run.TriggerPayload, &payload); err != nil {
		t.Fatalf("payload decode: %v body=%s", err, string(run.TriggerPayload))
	}
	if payload.Event != "demo.received" {
		t.Fatalf("envelope event: %q", payload.Event)
	}
	if payload.EventPayload["k"] != "v" {
		t.Fatalf("envelope payload: %#v", payload.EventPayload)
	}

	// last_fired_at must have been bumped.
	trigRow, err := testHandler.Queries.GetAutopilotTrigger(context.Background(), parseUUID(trig.ID))
	if err != nil {
		t.Fatalf("load trigger: %v", err)
	}
	if !trigRow.LastFiredAt.Valid {
		t.Fatal("last_fired_at should be set after webhook dispatch")
	}
}

func TestWebhookHandler_GitHubHeaderInferredEvent(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)

	w := postWebhook(t, *trig.WebhookToken, map[string]any{
		"action": "opened",
		"pull_request": map[string]any{
			"number": 42,
		},
	}, map[string]string{"X-GitHub-Event": "pull_request"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	resp := decodeWebhookResponse(t, w)
	runID := resp["run_id"].(string)
	run, err := testHandler.Queries.GetAutopilotRun(context.Background(), parseUUID(runID))
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	var env struct {
		Event string `json:"event"`
	}
	_ = json.Unmarshal(run.TriggerPayload, &env)
	if env.Event != "github.pull_request.opened" {
		t.Fatalf("event inference: got %q", env.Event)
	}
}

func TestWebhookHandler_RateLimitReturns429(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "paused") // paused → cheap ignored path
	trig := createWebhookTrigger(t, apID)

	prev := testHandler.WebhookRateLimiter
	testHandler.WebhookRateLimiter = newMemoryWebhookRateLimiter(webhookRateLimit{Limit: 2, Window: 60_000_000_000})
	t.Cleanup(func() { testHandler.WebhookRateLimiter = prev })

	for i := 0; i < 2; i++ {
		w := postWebhook(t, *trig.WebhookToken, map[string]any{"i": i}, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, w.Code)
		}
	}
	w := postWebhook(t, *trig.WebhookToken, map[string]any{"i": "third"}, nil)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRotateWebhookToken_ReplacesOldToken(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)
	oldToken := *trig.WebhookToken

	w := requestRotateWebhookToken(t, apID, trig.ID, uuid.NewString())
	if w.Code != http.StatusOK {
		t.Fatalf("rotate: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var rotated AutopilotTriggerResponse
	_ = json.Unmarshal(w.Body.Bytes(), &rotated)
	if rotated.WebhookToken == nil || *rotated.WebhookToken == oldToken {
		t.Fatalf("rotate did not change token: old=%q new=%v", oldToken, rotated.WebhookToken)
	}

	// Old token should now 404.
	resOld := postWebhook(t, oldToken, map[string]any{"x": 1}, nil)
	if resOld.Code != http.StatusNotFound {
		t.Fatalf("old token should be 404, got %d", resOld.Code)
	}
	// New token should accept.
	resNew := postWebhook(t, *rotated.WebhookToken, map[string]any{"x": 1}, nil)
	if resNew.Code != http.StatusOK {
		t.Fatalf("new token should be 200, got %d body=%s", resNew.Code, resNew.Body.String())
	}
}

func TestRotateWebhookToken_ReplaysCommittedRotation(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)
	requestKey := uuid.NewString()

	rotate := func() AutopilotTriggerResponse {
		t.Helper()
		w := requestRotateWebhookToken(t, apID, trig.ID, requestKey)
		if w.Code != http.StatusOK {
			t.Fatalf("rotate status = %d: %s", w.Code, w.Body.String())
		}
		return decodeAutopilotTriggerResponse(t, w)
	}

	first := rotate()
	replay := rotate()
	if !reflect.DeepEqual(replay, first) {
		t.Fatalf("replayed rotation = %#v, want exact %#v", replay, first)
	}
	other := createWebhookTrigger(t, apID)
	conflict := requestRotateWebhookToken(t, apID, other.ID, requestKey)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("same key for another trigger = %d %s, want 409", conflict.Code, conflict.Body.String())
	}
}

func TestRotateWebhookToken_ConcurrentReplayRotatesOnce(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)
	requestKey := uuid.NewString()

	const workers = 8
	responses := make(chan AutopilotTriggerResponse, workers)
	statuses := make(chan int, workers)
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(workers)
	for range workers {
		go func() {
			defer done.Done()
			start.Wait()
			w := requestRotateWebhookToken(t, apID, trig.ID, requestKey)
			var response AutopilotTriggerResponse
			_ = json.Unmarshal(w.Body.Bytes(), &response)
			statuses <- w.Code
			responses <- response
		}()
	}
	start.Done()
	done.Wait()
	close(statuses)
	close(responses)

	for status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("concurrent rotation status = %d, want 200", status)
		}
	}
	var first AutopilotTriggerResponse
	for response := range responses {
		if first.ID == "" {
			first = response
		} else if !reflect.DeepEqual(response, first) {
			t.Fatalf("concurrent rotation responses differ: %#v and %#v", first, response)
		}
	}
	var requests int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM autopilot_trigger_rotation_request WHERE idempotency_key = $1`, requestKey).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("rotation requests = %d, want 1", requests)
	}
}

func TestRotateWebhookToken_CompletionFailureRollsBackToken(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)
	oldToken := *trig.WebhookToken
	requestKey := uuid.NewString()
	suffix := uuid.NewString()
	functionName := quoteIdentifier("fail_webhook_rotation_completion_" + suffix)
	triggerName := quoteIdentifier("fail_webhook_rotation_completion_trigger_" + suffix)
	if _, err := testPool.Exec(context.Background(), fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.idempotency_key = %s::uuid THEN
				RAISE EXCEPTION 'forced webhook rotation completion failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON autopilot_trigger_rotation_request
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, quoteSQLLiteral(requestKey), triggerName, functionName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON autopilot_trigger_rotation_request`, triggerName))
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})

	w := requestRotateWebhookToken(t, apID, trig.ID, requestKey)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("forced completion failure = %d %s, want 500", w.Code, w.Body.String())
	}
	var token string
	var requests int
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT webhook_token FROM autopilot_trigger WHERE id = $1),
			(SELECT count(*) FROM autopilot_trigger_rotation_request WHERE idempotency_key = $2)
	`, trig.ID, requestKey).Scan(&token, &requests); err != nil {
		t.Fatal(err)
	}
	if token != oldToken || requests != 0 {
		t.Fatalf("failed rotation left token_changed=%t requests=%d, want false/0", token != oldToken, requests)
	}
}

func TestWebhookHandler_ArchivedAutopilotReturnsIgnored(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)

	if _, err := testPool.Exec(context.Background(),
		`UPDATE autopilot SET status = 'archived' WHERE id = $1`, apID); err != nil {
		t.Fatalf("archive autopilot: %v", err)
	}

	w := postWebhook(t, *trig.WebhookToken, map[string]any{"x": 1}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	resp := decodeWebhookResponse(t, w)
	if resp["status"] != "ignored" || resp["reason"] != "autopilot_archived" {
		t.Fatalf("expected ignored/autopilot_archived, got %#v", resp)
	}
}

func TestWebhookHandler_IPRateLimitReturns429BeforeDBLookup(t *testing.T) {
	// Spray random (likely-unknown) tokens from one IP and prove the IP
	// limiter trips before we exhaust the budget — without this gate an
	// attacker can probe the trigger-lookup index unboundedly. Rate-limit
	// keying is by the real source IP (r.RemoteAddr) since TrustedProxies
	// is empty here, so the bucket is per-connection — exactly the
	// property the per-IP limiter is meant to provide.
	prev := testHandler.WebhookIPRateLimiter
	testHandler.WebhookIPRateLimiter = newMemoryWebhookRateLimiter(webhookRateLimit{Limit: 2, Window: 60_000_000_000})
	t.Cleanup(func() { testHandler.WebhookIPRateLimiter = prev })

	post := func(token string) int {
		req := httptest.NewRequest("POST", "/api/webhooks/autopilots/"+token,
			bytes.NewBufferString(`{"x":1}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.0.2.7:1234" // stable source, three calls = same bucket
		req = withURLParam(req, "token", token)
		w := httptest.NewRecorder()
		testHandler.HandleAutopilotWebhook(w, req)
		return w.Code
	}

	if got := post("awt_unknown_a"); got != http.StatusNotFound {
		t.Fatalf("first probe: expected 404, got %d", got)
	}
	if got := post("awt_unknown_b"); got != http.StatusNotFound {
		t.Fatalf("second probe: expected 404, got %d", got)
	}
	if got := post("awt_unknown_c"); got != http.StatusTooManyRequests {
		t.Fatalf("third probe: expected 429 (IP bucket), got %d", got)
	}
}

func TestWebhookHandler_IPRateLimitNotBypassedByXFFSpoof(t *testing.T) {
	// With no trusted proxies, spoofed X-Forwarded-For values must not
	// bypass the real source IP's rate-limit bucket.
	prev := testHandler.WebhookIPRateLimiter
	testHandler.WebhookIPRateLimiter = newMemoryWebhookRateLimiter(webhookRateLimit{Limit: 2, Window: 60_000_000_000})
	t.Cleanup(func() { testHandler.WebhookIPRateLimiter = prev })

	post := func(token, xff string) int {
		req := httptest.NewRequest("POST", "/api/webhooks/autopilots/"+token,
			bytes.NewBufferString(`{"x":1}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", xff) // <-- attacker-controlled
		req.RemoteAddr = "198.51.100.42:5555"  // real (untrusted) source
		req = withURLParam(req, "token", token)
		w := httptest.NewRecorder()
		testHandler.HandleAutopilotWebhook(w, req)
		return w.Code
	}

	if got := post("awt_unknown_x", "1.1.1.1"); got != http.StatusNotFound {
		t.Fatalf("first probe: expected 404, got %d", got)
	}
	if got := post("awt_unknown_y", "2.2.2.2"); got != http.StatusNotFound {
		t.Fatalf("second probe: expected 404, got %d", got)
	}
	// Third request with yet another spoofed XFF — would have bypassed
	// the limiter under the old (header-trusting) behavior, but with the
	// CIDR-gated trust the bucket is still the real source IP.
	if got := post("awt_unknown_z", "3.3.3.3"); got != http.StatusTooManyRequests {
		t.Fatalf("third probe: expected 429 (bucket keyed by real IP), got %d", got)
	}
}

func TestCreateAutopilotTrigger_RejectsUnknownKind(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")

	w := requestCreateAutopilotTrigger(t, apID, map[string]any{
		"kind": "poll",
	}, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on unknown kind, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "schedule or webhook") {
		t.Fatalf("expected message to name allowed kinds, body=%s", w.Body.String())
	}
}

func TestCreateAutopilotTrigger_RejectsWebhookWithTimezone(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")

	w := requestCreateAutopilotTrigger(t, apID, map[string]any{
		"kind":     "webhook",
		"timezone": "Europe/Berlin",
	}, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on webhook+timezone, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateAutopilotTrigger_RejectsCronExpressionOnWebhookKind(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)

	w := requestUpdateAutopilotTrigger(t, apID, trig.ID, map[string]any{
		"cron_expression": "0 0 * * *",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on cron_expression for webhook trigger, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cron_expression") {
		t.Fatalf("error message should mention cron_expression, got %s", w.Body.String())
	}
}

func TestUpdateAutopilotTrigger_RejectsTimezoneOnWebhookKind(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)

	w := requestUpdateAutopilotTrigger(t, apID, trig.ID, map[string]any{
		"timezone": "Europe/Berlin",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on timezone for webhook trigger, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "timezone") {
		t.Fatalf("error message should mention timezone, got %s", w.Body.String())
	}
}

func TestUpdateAutopilotTrigger_AcceptsEnabledAndLabelOnWebhookKind(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)

	w := requestUpdateAutopilotTrigger(t, apID, trig.ID, map[string]any{
		"enabled": false,
		"label":   "renamed",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on enabled+label PATCH, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestGetAutopilotRun_ReturnsFullPayload(t *testing.T) {
	apID := createWebhookTestAutopilot(t, "active")
	trig := createWebhookTrigger(t, apID)

	post := postWebhook(t, *trig.WebhookToken, map[string]any{
		"event":        "demo.x",
		"eventPayload": map[string]any{"answer": 42},
	}, nil)
	if post.Code != http.StatusOK {
		t.Fatalf("seed webhook: %d body=%s", post.Code, post.Body.String())
	}
	seedResp := decodeWebhookResponse(t, post)
	runID := seedResp["run_id"].(string)

	wList := httptest.NewRecorder()
	reqList := newRequest("GET", "/api/autopilots/"+apID+"/runs", nil)
	reqList = withURLParam(reqList, "id", apID)
	testHandler.ListAutopilotRuns(wList, reqList)
	if wList.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d body=%s", wList.Code, wList.Body.String())
	}
	if strings.Contains(wList.Body.String(), `"answer":42`) {
		t.Fatalf("list response should NOT carry trigger_payload, body=%s", wList.Body.String())
	}

	wDetail := httptest.NewRecorder()
	reqDetail := newRequest("GET", "/api/autopilots/"+apID+"/runs/"+runID, nil)
	reqDetail = withURLParams(reqDetail, "id", apID, "runId", runID)
	testHandler.GetAutopilotRun(wDetail, reqDetail)
	if wDetail.Code != http.StatusOK {
		t.Fatalf("detail: expected 200, got %d body=%s", wDetail.Code, wDetail.Body.String())
	}
	if !strings.Contains(wDetail.Body.String(), `"answer":42`) {
		t.Fatalf("detail response should carry full trigger_payload, body=%s", wDetail.Body.String())
	}
}
