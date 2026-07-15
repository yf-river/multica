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
		{"runtime_provider", runtimeProviderLabels.normalize, "provider-from-user-input", "other"},
		{"runtime_mode", runtimeModeLabels.normalize, "workspace-123", "unknown"},
		{"task_source", taskSourceLabels.normalize, "task-123", "other"},
		{"platform", platformLabels.normalize, "iphone-internal-build-9", "unknown"},
		{"platform_known", platformLabels.normalize, "web", "web"},
		{"autopilot_cadence", autopilotCadenceLabels.normalize, "every_5_min", "unknown"},
		{"autopilot_trigger", autopilotTriggerLabels.normalize, "future_kind", "unknown"},
		{"autopilot_skip_reason", autopilotSkipReasonLabels.normalize, "lunar_phase", "other"},
		{"webhook_provider", webhookProviderLabels.normalize, "internal-billing", "other"},
		{"webhook_status", webhookDeliveryStatusLabels.normalize, "exotic", "other"},
		{"daemon_ws_kind", daemonWSKindLabels.normalize, "future_event", "other"},
		{"feedback_kind", feedbackKindLabels.normalize, "rant", "other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.fn(test.input); got != test.want {
				t.Fatalf("normalize(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}
