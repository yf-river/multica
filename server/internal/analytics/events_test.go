package analytics

import (
	"reflect"
	"testing"
)

func TestRuntimeReadyOmitsUnmeasuredDuration(t *testing.T) {
	ev := RuntimeReady("codex", 0)
	if _, ok := ev.Properties["ready_duration_ms"]; ok {
		t.Fatalf("ready_duration_ms should be omitted until it is measured")
	}

	ev = RuntimeReady("codex", 123)
	if got := ev.Properties["ready_duration_ms"]; got != int64(123) {
		t.Fatalf("ready_duration_ms = %v, want 123", got)
	}
}

func TestMetricsOnlyEventsContainOnlyMetricInputs(t *testing.T) {
	cases := []Event{
		RuntimeRegistered("codex"),
		RuntimeReady("codex", 123),
		RuntimeFailed("codex", "registration_failed", true),
		RuntimeOffline("codex"),
		AutopilotRunStarted("manual"),
		AutopilotRunCompleted("manual"),
		AutopilotRunFailed("manual"),
	}
	for _, event := range cases {
		var want map[string]any
		switch event.Name {
		case EventRuntimeReady:
			want = map[string]any{"runtime_mode": "local", "provider": "codex", "ready_duration_ms": int64(123)}
		case EventRuntimeFailed:
			want = map[string]any{"runtime_mode": "local", "provider": "codex", "failure_reason": "registration_failed", "recoverable": true}
		case EventRuntimeRegistered, EventRuntimeOffline:
			want = map[string]any{"runtime_mode": "local", "provider": "codex"}
		default:
			want = map[string]any{"cadence": "manual", "trigger_kind": "manual"}
		}
		if !reflect.DeepEqual(event.Properties, want) {
			t.Errorf("%s properties = %#v, want %#v", event.Name, event.Properties, want)
		}
	}
}

func TestIsMetricsOnly(t *testing.T) {
	// Operational / execution-lifecycle events are Prometheus-only and must
	// not be shipped to PostHog.
	for _, name := range []string{
		EventRuntimeRegistered, EventRuntimeReady, EventRuntimeFailed, EventRuntimeOffline,
		EventAutopilotRunStarted, EventAutopilotRunCompleted, EventAutopilotRunFailed,
	} {
		if !IsMetricsOnly(name) {
			t.Errorf("IsMetricsOnly(%q) = false, want true (operational event must stay out of PostHog)", name)
		}
	}
	// Product-behaviour events must still reach PostHog.
	for _, name := range []string{
		EventSignup, EventWorkspaceCreated, EventIssueCreated, EventIssueExecuted,
		EventChatMessageSent, EventAgentCreated, EventAutopilotCreated,
	} {
		if IsMetricsOnly(name) {
			t.Errorf("IsMetricsOnly(%q) = true, want false (product event must reach PostHog)", name)
		}
	}
}
