package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestGenerateWebhookToken_PrefixAndLength(t *testing.T) {
	token, err := generateWebhookToken()
	if err != nil {
		t.Fatalf("generateWebhookToken: %v", err)
	}
	if !strings.HasPrefix(token, "awt_") {
		t.Fatalf("expected awt_ prefix, got %q", token)
	}
	// 32 random bytes -> 43 base64-url chars (no padding).
	if len(token) != len("awt_")+43 {
		t.Fatalf("unexpected token length: %d (token=%q)", len(token), token)
	}
}

func TestGenerateWebhookToken_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 128)
	for i := 0; i < 128; i++ {
		token, err := generateWebhookToken()
		if err != nil {
			t.Fatalf("generateWebhookToken: %v", err)
		}
		if _, dup := seen[token]; dup {
			t.Fatalf("duplicate token after %d generations: %q", i, token)
		}
		seen[token] = struct{}{}
	}
}

func TestGenerateWebhookToken_NoUnsafeURLChars(t *testing.T) {
	token, err := generateWebhookToken()
	if err != nil {
		t.Fatalf("generateWebhookToken: %v", err)
	}
	if strings.ContainsAny(token, "+/= ") {
		t.Fatalf("token has unsafe characters: %q", token)
	}
}

func TestNormalizeWebhookPayload_PreservesCallerProvidedEnvelope(t *testing.T) {
	body := []byte(`{"event":"caller.event","eventPayload":{"k":"v"}}`)
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")

	env, err := normalizeWebhookPayload(body, headers)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if env.Event != "caller.event" {
		t.Fatalf("event: got %q want %q", env.Event, "caller.event")
	}
	var inner map[string]string
	if err := json.Unmarshal(env.EventPayload, &inner); err != nil {
		t.Fatalf("eventPayload not preserved: %v", err)
	}
	if inner["k"] != "v" {
		t.Fatalf("eventPayload contents lost: %#v", inner)
	}
	if env.Request.ContentType != "application/json" {
		t.Fatalf("contentType: %q", env.Request.ContentType)
	}
	if env.Request.ReceivedAt == "" {
		t.Fatal("receivedAt not set")
	}
}

func TestNormalizeWebhookPayload_GitHubHeaderInferEvent(t *testing.T) {
	body := []byte(`{"action":"opened","pull_request":{"number":7}}`)
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("X-GitHub-Event", "pull_request")

	env, err := normalizeWebhookPayload(body, headers)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if env.Event != "github.pull_request.opened" {
		t.Fatalf("github event: got %q", env.Event)
	}
	// Original body preserved in eventPayload.
	if !strings.Contains(string(env.EventPayload), `"pull_request"`) {
		t.Fatalf("body not preserved in eventPayload: %s", env.EventPayload)
	}
}

func TestNormalizeWebhookPayloadEventPrecedence(t *testing.T) {
	for _, testCase := range []struct {
		name, body, header, headerValue, want string
	}{
		{name: "GitLab header", body: `{"object_kind":"push"}`, header: "X-Gitlab-Event", headerValue: "Push Hook", want: "gitlab.Push Hook"},
		{name: "generic header", body: `{"a":1}`, header: "X-Event-Type", headerValue: "custom.thing", want: "custom.thing"},
		{name: "GitHub header without action", body: `{"some":"thing"}`, header: "X-GitHub-Event", headerValue: "push", want: "github.push"},
		{name: "event field", body: `{"event":"demo.received","data":{"x":1}}`, want: "demo.received"},
		{name: "type field", body: `{"type":"foo.bar"}`, want: "foo.bar"},
		{name: "action field", body: `{"action":"opened"}`, want: "opened"},
		{name: "default", body: `{"foo":"bar"}`, want: "webhook.received"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			headers := http.Header{}
			if testCase.header != "" {
				headers.Set(testCase.header, testCase.headerValue)
			}
			envelope, err := normalizeWebhookPayload([]byte(testCase.body), headers)
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if envelope.Event != testCase.want {
				t.Fatalf("event = %q, want %q", envelope.Event, testCase.want)
			}
			if string(envelope.EventPayload) != testCase.body {
				t.Fatalf("event payload = %s, want %s", envelope.EventPayload, testCase.body)
			}
		})
	}
}

func TestNormalizeWebhookPayload_PreservesArray(t *testing.T) {
	body := []byte(`[{"a":1},{"b":2}]`)
	env, err := normalizeWebhookPayload(body, http.Header{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if env.Event != "webhook.received" {
		t.Fatalf("array event: %q", env.Event)
	}
	var arr []map[string]int
	if err := json.Unmarshal(env.EventPayload, &arr); err != nil {
		t.Fatalf("array not preserved: %v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("array length: %d", len(arr))
	}
}

func TestNormalizeWebhookPayload_RejectsInvalidJSON(t *testing.T) {
	if _, err := normalizeWebhookPayload([]byte(`not json`), http.Header{}); err == nil {
		t.Fatal("expected error on invalid JSON")
	}
}

func TestNormalizeWebhookPayload_RejectsScalarBody(t *testing.T) {
	if _, err := normalizeWebhookPayload([]byte(`"hello"`), http.Header{}); err == nil {
		t.Fatal("expected error on scalar JSON body")
	}
}

func TestWebhookEventAllowedByTriggerScope_NoFiltersAllowsAll(t *testing.T) {
	if !webhookEventAllowedByTriggerScope(nil, WebhookEnvelope{Event: "github.push"}) {
		t.Fatal("nil filters should allow all")
	}
	if !webhookEventAllowedByTriggerScope([]byte{}, WebhookEnvelope{Event: "github.push"}) {
		t.Fatal("empty filters should allow all")
	}
	if !webhookEventAllowedByTriggerScope([]byte("[]"), WebhookEnvelope{Event: "github.push"}) {
		t.Fatal("empty JSON array should allow all")
	}
}

func TestWebhookEventAllowedByTriggerScope_FiltersUndeclaredEvent(t *testing.T) {
	filters := []byte(`[{"event":"workflow_run","actions":["completed"]}]`)
	env := WebhookEnvelope{Event: "github.push", EventPayload: json.RawMessage(`{"action":"pushed"}`)}
	if webhookEventAllowedByTriggerScope(filters, env) {
		t.Fatal("undeclared event should be filtered")
	}
}

func TestWebhookEventAllowedByTriggerScope_FiltersUndeclaredAction(t *testing.T) {
	filters := []byte(`[{"event":"workflow_run","actions":["completed"]}]`)
	env := WebhookEnvelope{Event: "github.workflow_run.in_progress", EventPayload: json.RawMessage(`{"action":"in_progress"}`)}
	if webhookEventAllowedByTriggerScope(filters, env) {
		t.Fatal("undeclared action should be filtered")
	}
}

func TestWebhookEventAllowedByTriggerScope_AllowsDeclaredAction(t *testing.T) {
	filters := []byte(`[{"event":"workflow_run","actions":["completed"]}]`)
	env := WebhookEnvelope{Event: "github.workflow_run.completed", EventPayload: json.RawMessage(`{"action":"completed"}`)}
	if !webhookEventAllowedByTriggerScope(filters, env) {
		t.Fatal("declared action should be allowed")
	}
}

func TestWebhookEventAllowedByTriggerScope_AnyActionWhenEmpty(t *testing.T) {
	filters := []byte(`[{"event":"workflow_run"}]`)
	env := WebhookEnvelope{Event: "github.workflow_run.in_progress", EventPayload: json.RawMessage(`{"action":"in_progress"}`)}
	if !webhookEventAllowedByTriggerScope(filters, env) {
		t.Fatal("empty actions should allow any action for the event")
	}
}

func TestWebhookEventAllowedByTriggerScope_MultipleFiltersSameEvent(t *testing.T) {
	filters := []byte(`[
		{"event":"workflow_run","actions":["completed"]},
		{"event":"workflow_run","actions":["requested"]}
	]`)

	completed := WebhookEnvelope{
		Event:        "github.workflow_run.completed",
		EventPayload: json.RawMessage(`{"action":"completed"}`),
	}
	if !webhookEventAllowedByTriggerScope(filters, completed) {
		t.Fatal("workflow_run.completed should match the first filter")
	}

	requested := WebhookEnvelope{
		Event:        "github.workflow_run.requested",
		EventPayload: json.RawMessage(`{"action":"requested"}`),
	}
	if !webhookEventAllowedByTriggerScope(filters, requested) {
		t.Fatal("workflow_run.requested should match the second filter — pre-fix this silently dropped")
	}

	inProgress := WebhookEnvelope{
		Event:        "github.workflow_run.in_progress",
		EventPayload: json.RawMessage(`{"action":"in_progress"}`),
	}
	if webhookEventAllowedByTriggerScope(filters, inProgress) {
		t.Fatal("workflow_run.in_progress is in neither filter and should be filtered out")
	}
}

func TestWebhookEventAllowedByTriggerScope_MalformedDenies(t *testing.T) {
	corrupt := []byte(`{not a json array}`)
	env := WebhookEnvelope{
		Event:        "github.workflow_run.completed",
		EventPayload: json.RawMessage(`{"action":"completed"}`),
	}
	if webhookEventAllowedByTriggerScope(corrupt, env) {
		t.Fatal("malformed event_filters must fail closed (deny), never widen the allowlist")
	}
}

func TestWebhookEventAllowedByTriggerScope_MultipleFilters(t *testing.T) {
	filters := []byte(`[{"event":"workflow_run","actions":["completed"]},{"event":"check_suite","actions":["completed","failure"]}]`)

	allowed1 := WebhookEnvelope{Event: "github.check_suite.completed", EventPayload: json.RawMessage(`{"action":"completed"}`)}
	if !webhookEventAllowedByTriggerScope(filters, allowed1) {
		t.Fatal("check_suite.completed should be allowed")
	}

	allowed2 := WebhookEnvelope{Event: "github.check_suite.failure", EventPayload: json.RawMessage(`{"action":"failure"}`)}
	if !webhookEventAllowedByTriggerScope(filters, allowed2) {
		t.Fatal("check_suite.failure should be allowed")
	}

	filtered := WebhookEnvelope{Event: "github.check_suite.requested", EventPayload: json.RawMessage(`{"action":"requested"}`)}
	if webhookEventAllowedByTriggerScope(filters, filtered) {
		t.Fatal("check_suite.requested should be filtered")
	}
}

func TestSplitWebhookEvent(t *testing.T) {
	tests := []struct {
		input        string
		wantProvider string
		wantName     string
		wantAction   string
	}{
		{"github.workflow_run.completed", "github", "workflow_run", "completed"},
		{"github.push", "github", "push", ""},
		{"gitlab.Merge Request Hook", "gitlab", "Merge Request Hook", ""},
		{"webhook.received", "", "webhook", "received"},
		{"custom", "", "custom", ""},
	}
	for _, tc := range tests {
		p, n, a := splitWebhookEvent(tc.input)
		if p != tc.wantProvider || n != tc.wantName || a != tc.wantAction {
			t.Fatalf("splitWebhookEvent(%q) = (%q, %q, %q), want (%q, %q, %q)",
				tc.input, p, n, a, tc.wantProvider, tc.wantName, tc.wantAction)
		}
	}
}
