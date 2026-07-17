package service

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTaskFailureReasonForAutopilotRun(t *testing.T) {
	cases := []struct {
		name string
		task db.AgentTaskQueue
		want string
	}{
		{
			name: "prefers raw error text",
			task: db.AgentTaskQueue{
				Error:         pgtype.Text{String: "tests failed", Valid: true},
				FailureReason: pgtype.Text{String: "agent_error", Valid: true},
			},
			want: "tests failed",
		},
		{
			name: "falls back to classified reason when error is blank",
			task: db.AgentTaskQueue{
				Error:         pgtype.Text{String: "   ", Valid: true},
				FailureReason: pgtype.Text{String: "codex_semantic_inactivity", Valid: true},
			},
			want: "codex_semantic_inactivity",
		},
		{
			name: "generic default when nothing is set",
			task: db.AgentTaskQueue{},
			want: "task failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskFailureReasonForAutopilotRun(tc.task); got != tc.want {
				t.Fatalf("taskFailureReasonForAutopilotRun() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildIssueDescription_NoTriggerPayload(t *testing.T) {
	s := &AutopilotService{}
	ap := db.Autopilot{Description: pgtype.Text{String: "do the thing", Valid: true}}
	run := db.AutopilotRun{Source: "schedule", TriggeredAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}}

	got, err := s.buildIssueDescription(ap, run, "UTC")
	if err != nil {
		t.Fatalf("buildIssueDescription: %v", err)
	}
	if !strings.HasPrefix(got.String, "do the thing") {
		t.Fatalf("description should preserve user description: %q", got.String)
	}
	if !strings.Contains(got.String, "Autopilot run triggered at") {
		t.Fatalf("description should include schedule note: %q", got.String)
	}
	if strings.Contains(got.String, "Webhook event") {
		t.Fatalf("description must not mention webhook for non-webhook source: %q", got.String)
	}
}

func TestBuildIssueDescription_UsesTriggerTimezone(t *testing.T) {
	s := &AutopilotService{}
	ap := db.Autopilot{Description: pgtype.Text{String: "daily sync", Valid: true}}
	run := db.AutopilotRun{
		Source:      "schedule",
		TriggeredAt: pgtype.Timestamptz{Time: time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC), Valid: true},
	}

	got, err := s.buildIssueDescription(ap, run, "Asia/Tokyo")
	if err != nil {
		t.Fatalf("buildIssueDescription: %v", err)
	}
	if !strings.Contains(got.String, "Autopilot run triggered at 2026-05-26 09:00 Asia/Tokyo") {
		t.Fatalf("description should use trigger timezone: %q", got.String)
	}
	if strings.Contains(got.String, "2026-05-26 00:00 UTC") {
		t.Fatalf("description must not render the trigger time in UTC when trigger timezone is known: %q", got.String)
	}
}

func TestBuildIssueDescription_RejectsInvalidTriggerTimezone(t *testing.T) {
	s := &AutopilotService{}
	ap := db.Autopilot{Description: pgtype.Text{String: "do the thing", Valid: true}}
	run := db.AutopilotRun{
		Source:      "schedule",
		TriggeredAt: pgtype.Timestamptz{Time: time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC), Valid: true},
	}

	if _, err := s.buildIssueDescription(ap, run, "Foo/Bar"); err == nil {
		t.Fatal("invalid trigger timezone should fail")
	}
}

func TestInterpolateTemplate_RejectsInvalidTriggerTimezone(t *testing.T) {
	s := &AutopilotService{}
	ap := db.Autopilot{
		Title:              "fallback",
		IssueTitleTemplate: pgtype.Text{String: "report {{date}}", Valid: true},
	}
	run := db.AutopilotRun{
		TriggeredAt: pgtype.Timestamptz{Time: time.Date(2026, 5, 26, 23, 30, 0, 0, time.UTC), Valid: true},
	}

	if _, err := s.interpolateTemplate(ap, run, "Foo/Bar"); err == nil {
		t.Fatal("invalid trigger timezone should fail")
	}
}

func TestBuildIssueDescription_WithWebhookPayload(t *testing.T) {
	s := &AutopilotService{}
	ap := db.Autopilot{Description: pgtype.Text{String: "watch PRs", Valid: true}}
	payload := []byte(`{"event":"github.pull_request.opened","eventPayload":{"number":7},"request":{"receivedAt":"2026-05-09T00:00:00Z","contentType":"application/json"}}`)
	run := db.AutopilotRun{Source: "webhook", TriggerPayload: payload, TriggeredAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}}

	got, err := s.buildIssueDescription(ap, run, "UTC")
	if err != nil {
		t.Fatalf("buildIssueDescription: %v", err)
	}
	if !strings.HasPrefix(got.String, "watch PRs") {
		t.Fatalf("user description not preserved: %q", got.String)
	}
	if !strings.Contains(got.String, "Webhook event: github.pull_request.opened") {
		t.Fatalf("description should include webhook event line: %q", got.String)
	}
	if !strings.Contains(got.String, "\"number\": 7") && !strings.Contains(got.String, "\"number\":7") {
		t.Fatalf("description should include payload json: %q", got.String)
	}
	// Italic schedule line must come before the webhook block.
	idxItalic := strings.Index(got.String, "*Autopilot run triggered")
	idxWebhook := strings.Index(got.String, "Webhook event")
	if idxItalic < 0 || idxWebhook < 0 || idxItalic > idxWebhook {
		t.Fatalf("italic line should appear before webhook block: %q", got.String)
	}
}

func TestBuildIssueDescription_WebhookSourceMissingEnvelope(t *testing.T) {
	// Defensive: if a future caller stuffs a non-envelope JSON object into
	// trigger_payload, we should still emit a webhook block with sensible
	// defaults rather than skipping the section entirely.
	s := &AutopilotService{}
	ap := db.Autopilot{Description: pgtype.Text{String: "thing", Valid: true}}
	payload := []byte(`{"raw":"missing envelope"}`)
	run := db.AutopilotRun{Source: "webhook", TriggerPayload: payload, TriggeredAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}}

	got, err := s.buildIssueDescription(ap, run, "UTC")
	if err != nil {
		t.Fatalf("buildIssueDescription: %v", err)
	}
	if !strings.Contains(got.String, "Webhook event:") {
		t.Fatalf("should still emit webhook block: %q", got.String)
	}
}

func TestBuildIssueDescription_RejectsMalformedPersistedWebhookPayload(t *testing.T) {
	s := &AutopilotService{}
	ap := db.Autopilot{Description: pgtype.Text{String: "thing", Valid: true}}
	run := db.AutopilotRun{
		Source:         "webhook",
		TriggerPayload: []byte(`not-json`),
		TriggeredAt:    pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}

	if _, err := s.buildIssueDescription(ap, run, "UTC"); err == nil {
		t.Fatal("malformed persisted webhook payload should fail")
	}
}

func TestValidateAutopilotTriggerPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr bool
	}{
		{name: "absent", payload: nil},
		{name: "object", payload: []byte(`{"event":"manual"}`)},
		{name: "array", payload: []byte(`[]`), wantErr: true},
		{name: "scalar", payload: []byte(`true`), wantErr: true},
		{name: "null", payload: []byte(`null`), wantErr: true},
		{name: "malformed", payload: []byte(`{`), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateAutopilotTriggerPayload(test.payload)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateAutopilotTriggerPayload(%q) error = %v, wantErr %v", test.payload, err, test.wantErr)
			}
		})
	}
}

func TestBuildIssueDescription_NonWebhookSourceWithPayloadIgnored(t *testing.T) {
	// Manual / schedule with a payload should not get a webhook block.
	s := &AutopilotService{}
	ap := db.Autopilot{Description: pgtype.Text{String: "thing", Valid: true}}
	run := db.AutopilotRun{Source: "manual", TriggerPayload: []byte(`{"event":"x.y"}`), TriggeredAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}}

	got, err := s.buildIssueDescription(ap, run, "UTC")
	if err != nil {
		t.Fatalf("buildIssueDescription: %v", err)
	}
	if strings.Contains(got.String, "Webhook event") {
		t.Fatalf("non-webhook source should not include webhook block: %q", got.String)
	}
}

func TestInterpolateTemplate(t *testing.T) {
	s := &AutopilotService{}
	run := db.AutopilotRun{TriggeredAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}}
	today := run.TriggeredAt.Time.UTC().Format("2006-01-02")

	cases := []struct {
		name   string
		ap     db.Autopilot
		expect string
	}{
		{
			name:   "date placeholder substituted",
			ap:     db.Autopilot{Title: "fallback", IssueTitleTemplate: pgtype.Text{String: "probe — {{date}}", Valid: true}},
			expect: "probe — " + today,
		},
		{
			name:   "date placeholder with whitespace substituted",
			ap:     db.Autopilot{Title: "fallback", IssueTitleTemplate: pgtype.Text{String: "probe — {{ date }}", Valid: true}},
			expect: "probe — " + today,
		},
		{
			name:   "empty template falls back to autopilot title",
			ap:     db.Autopilot{Title: "fallback title", IssueTitleTemplate: pgtype.Text{Valid: false}},
			expect: "fallback title",
		},
		{
			name:   "template without placeholder is returned verbatim",
			ap:     db.Autopilot{Title: "fallback", IssueTitleTemplate: pgtype.Text{String: "static title", Valid: true}},
			expect: "static title",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.interpolateTemplate(tc.ap, run, "UTC")
			if err != nil {
				t.Fatalf("interpolateTemplate: %v", err)
			}
			if got != tc.expect {
				t.Fatalf("interpolateTemplate = %q, want %q", got, tc.expect)
			}
		})
	}
}

func TestInterpolateTemplate_UsesTriggerTimezoneForDate(t *testing.T) {
	s := &AutopilotService{}
	ap := db.Autopilot{
		Title:              "fallback",
		IssueTitleTemplate: pgtype.Text{String: "Tokyo report {{date}}", Valid: true},
	}
	run := db.AutopilotRun{
		TriggeredAt: pgtype.Timestamptz{Time: time.Date(2026, 5, 26, 23, 30, 0, 0, time.UTC), Valid: true},
	}

	got, err := s.interpolateTemplate(ap, run, "Asia/Tokyo")
	if err != nil {
		t.Fatalf("interpolateTemplate: %v", err)
	}
	if want := "Tokyo report 2026-05-27"; got != want {
		t.Fatalf("interpolateTemplate = %q, want %q", got, want)
	}
}

func TestValidateIssueTitleTemplate(t *testing.T) {
	t.Run("accepts empty template", func(t *testing.T) {
		if err := ValidateIssueTitleTemplate(""); err != nil {
			t.Fatalf("empty template must be valid: %v", err)
		}
	})
	t.Run("accepts plain text", func(t *testing.T) {
		if err := ValidateIssueTitleTemplate("daily report"); err != nil {
			t.Fatalf("plain text must be valid: %v", err)
		}
	})
	t.Run("accepts {{date}}", func(t *testing.T) {
		if err := ValidateIssueTitleTemplate("probe — {{date}}"); err != nil {
			t.Fatalf("{{date}} must be valid: %v", err)
		}
	})
	t.Run("accepts {{ date }} with whitespace", func(t *testing.T) {
		if err := ValidateIssueTitleTemplate("probe — {{ date }}"); err != nil {
			t.Fatalf("{{ date }} must be valid: %v", err)
		}
	})

	rejections := []struct {
		name string
		tmpl string
		// nameInError is the offending variable name that must appear in the
		// returned error so CLI users see which token was rejected.
		nameInError string
	}{
		{"go template style", "probe — {{.TriggeredAt}}", ".TriggeredAt"},
		{"mustache style unknown variable", "probe — {{trigger_id}}", "trigger_id"},
		{"datetime not yet supported", "probe — {{datetime}}", "datetime"},
		{"empty placeholder", "probe — {{}}", ""},
		{"mixed valid + invalid still fails", "probe — {{date}} {{trigger_source}}", "trigger_source"},
	}
	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIssueTitleTemplate(tc.tmpl)
			if err == nil {
				t.Fatalf("expected rejection for %q", tc.tmpl)
			}
			if !strings.Contains(err.Error(), "unknown template variable") {
				t.Fatalf("error should mention unknown template variable: %v", err)
			}
			if tc.nameInError != "" && !strings.Contains(err.Error(), tc.nameInError) {
				t.Fatalf("error should name the offending token %q: %v", tc.nameInError, err)
			}
		})
	}
}
