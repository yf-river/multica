package metrics

import "testing"

func TestBusinessMetricLabelsRejectHighCardinalityNames(t *testing.T) {
	for metric, labels := range businessMetricLabels {
		for _, label := range labels {
			if _, forbidden := forbiddenMetricLabels[label]; forbidden {
				t.Fatalf("metric %s uses forbidden label %s", metric, label)
			}
		}
	}
}

func TestNormalizeLabelsCollapseUnknownValues(t *testing.T) {
	tests := []struct {
		name  string
		fn    func(string) string
		input string
		want  string
	}{
		{"runtime_provider", normalizeRuntimeProvider, "provider-from-user-input", "other"},
		{"runtime_mode", normalizeRuntimeMode, "workspace-123", "unknown"},
		{"task_source", normalizeTaskSource, "task-123", "other"},
		{"platform", normalizePlatform, "iphone-internal-build-9", "unknown"},
		{"platform_known", normalizePlatform, "web", "web"},
		{"autopilot_cadence", normalizeAutopilotCadence, "every_5_min", "unknown"},
		{"autopilot_trigger", normalizeAutopilotTrigger, "future_kind", "unknown"},
		{"autopilot_skip_reason", normalizeAutopilotSkipReason, "lunar_phase", "other"},
		{"webhook_provider", normalizeWebhookProvider, "internal-billing", "other"},
		{"webhook_status", normalizeWebhookDeliveryStatus, "exotic", "other"},
		{"daemon_ws_kind", normalizeDaemonWSKind, "future_event", "other"},
		{"feedback_kind", normalizeFeedbackKind, "rant", "other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.fn(test.input); got != test.want {
				t.Fatalf("normalize(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}
