package main

import "testing"

func TestAttachmentInputRequiresCurrentIssueAndEarlierCreation(t *testing.T) {
	before := "2026-10-25T00:00:00Z"
	after := "2026-10-25T00:01:00Z"
	for _, tc := range []struct {
		issue, task, created string
		want                 bool
	}{{"issue", "older", before, true}, {"other", "older", before, false}, {"issue", "current", before, false}, {"issue", "older", after, false}, {"issue", "older", "", false}} {
		got := attachmentIsRunInput(map[string]any{"issue_id": tc.issue, "task_id": tc.task, "created_at": tc.created}, "issue", "current", before)
		if got != tc.want {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
}
