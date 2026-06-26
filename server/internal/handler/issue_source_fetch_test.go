package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRecordIssueSourceFetchWritesMetadataAndTrace(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "Source fetch runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Source fetch agent")
	taskID := createDispatchedClaimFixtureTask(t, ctx, agentID, runtimeID, issueID, "1 second", true)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/source-fetch", map[string]any{
		"provider":      "tapd",
		"status":        "fetched",
		"workspace_id":  "47654106",
		"resource_type": "markdown_wiki",
		"resource_id":   "1147654106001004154",
		"title":         "用户快捷入口需求",
		"summary":       "快捷入口属于当前登录用户。",
		"body_excerpt":  "快捷入口属于当前登录用户，不同用户之间互不影响。",
		"version":       "2026-06-18 07:39:03",
		"duration_ms":   1234,
	})
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	req = withURLParam(req, "id", issueID)

	testHandler.RecordIssueSourceFetch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("RecordIssueSourceFetch: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Metadata   map[string]any `json:"metadata"`
		TraceEvent map[string]any `json:"trace_event"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Metadata["source_fetch_status"] != "fetched" {
		t.Fatalf("source_fetch_status = %v", resp.Metadata["source_fetch_status"])
	}
	if resp.Metadata["source_fetch_title"] != "用户快捷入口需求" {
		t.Fatalf("source_fetch_title = %v", resp.Metadata["source_fetch_title"])
	}
	if resp.Metadata["source_fetch_body_excerpt"] == "" {
		t.Fatalf("source_fetch_body_excerpt missing: %+v", resp.Metadata)
	}
	if resp.TraceEvent["event_type"] != "source.fetch" {
		t.Fatalf("trace event = %+v", resp.TraceEvent)
	}

	events, err := testHandler.Queries.ListIssueTaskTraceEvents(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatalf("ListIssueTaskTraceEvents: %v", err)
	}
	found := false
	for _, ev := range events {
		if ev.EventType == "source.fetch" && ev.TaskID == parseUUID(taskID) {
			found = true
			if ev.Status != "fetched" {
				t.Fatalf("source.fetch status = %q", ev.Status)
			}
			if ev.DurationMs.Int64 != 1234 || !ev.DurationMs.Valid {
				t.Fatalf("duration_ms = %+v", ev.DurationMs)
			}
		}
	}
	if !found {
		t.Fatalf("source.fetch trace event not found: %+v", events)
	}
}

func TestRecordIssueSourceFetchValidatesFetchedTitle(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "Source fetch validation runtime")
	_, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Source fetch validation agent")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/source-fetch", map[string]any{
		"provider": "tapd",
		"status":   "fetched",
	})
	req = withURLParam(req, "id", issueID)

	testHandler.RecordIssueSourceFetch(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("RecordIssueSourceFetch: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
