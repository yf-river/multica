package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestRecordIssueSourceFetchAutoFetchesTapdWikiWithAccountProfile(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `DELETE FROM external_credential_profile WHERE user_id = $1 AND provider = 'tapd'`, testUserID); err != nil {
		t.Fatalf("clear tapd profiles: %v", err)
	}
	t.Setenv("TAPD_AUTO_FETCH_TEST_TOKEN", "tapd-test-token")

	var sawAuth bool
	tapdAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tapd_wikis" {
			t.Fatalf("unexpected TAPD path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer tapd-test-token" && r.Header.Get("Via") == "mcp" {
			sawAuth = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": 1,
			"data": []any{map[string]any{
				"TWiki": map[string]any{
					"id":                   "1147654106001004154",
					"name":                 "用户快捷入口需求",
					"markdown_description": "快捷入口属于当前登录用户，不同用户之间互不影响。",
					"modified":             "2026-06-18 07:39:03",
				},
			}},
		})
	}))
	defer tapdAPI.Close()
	t.Setenv("TAPD_API_BASE_URL", tapdAPI.URL)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles", map[string]any{
		"provider":   "tapd",
		"name":       fmt.Sprintf("tapd-auto-fetch-%d", time.Now().UnixNano()),
		"secret_ref": "env:TAPD_AUTO_FETCH_TEST_TOKEN",
	})
	testHandler.CreateExternalCredentialProfile(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateExternalCredentialProfile: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	runtimeID := createClaimReclaimRuntime(t, ctx, "Source fetch auto runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Source fetch auto agent")
	taskID := createDispatchedClaimFixtureTask(t, ctx, agentID, runtimeID, issueID, "1 second", true)
	metadata := map[string]any{
		"source_url":         "https://www.tapd.cn/47654106/markdown_wikis/show/#1147654106001004154",
		"tapd_workspace_id":  "47654106",
		"tapd_resource_type": "markdown_wiki",
		"tapd_resource_id":   "1147654106001004154",
	}
	rawMetadata, _ := json.Marshal(metadata)
	if _, err := testPool.Exec(ctx, `UPDATE issue SET metadata = $2 WHERE id = $1`, issueID, rawMetadata); err != nil {
		t.Fatalf("set issue metadata: %v", err)
	}

	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/issues/"+issueID+"/source-fetch", map[string]any{
		"provider":   "tapd",
		"auto_fetch": true,
	})
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	req = withURLParam(req, "id", issueID)
	testHandler.RecordIssueSourceFetch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("RecordIssueSourceFetch auto_fetch: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !sawAuth {
		t.Fatal("TAPD auto_fetch did not send credential-backed MCP authorization headers")
	}
	if strings.Contains(w.Body.String(), "tapd-test-token") {
		t.Fatalf("source-fetch response leaked token: %s", w.Body.String())
	}
	var resp struct {
		Metadata   map[string]any `json:"metadata"`
		TraceEvent map[string]any `json:"trace_event"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Metadata["source_fetch_status"] != "fetched" || resp.Metadata["source_fetch_title"] != "用户快捷入口需求" {
		t.Fatalf("metadata = %+v", resp.Metadata)
	}
	if resp.TraceEvent["status"] != "fetched" {
		t.Fatalf("trace_event = %+v", resp.TraceEvent)
	}
}

func TestRecordIssueSourceFetchAutoFetchParsesTapdWikiSourceURL(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `DELETE FROM external_credential_profile WHERE user_id = $1 AND provider = 'tapd'`, testUserID); err != nil {
		t.Fatalf("clear tapd profiles: %v", err)
	}
	t.Setenv("TAPD_AUTO_FETCH_URL_TEST_TOKEN", "tapd-test-token")

	var sawAuth bool
	var sawWorkspaceID string
	var sawWikiID string
	tapdAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tapd_wikis" {
			t.Fatalf("unexpected TAPD path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer tapd-test-token" && r.Header.Get("Via") == "mcp" {
			sawAuth = true
		}
		sawWorkspaceID = r.URL.Query().Get("workspace_id")
		sawWikiID = r.URL.Query().Get("id")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": 1,
			"data": []any{map[string]any{
				"TWiki": map[string]any{
					"id":                   "1147654106001004223",
					"name":                 "增强密码强度",
					"markdown_description": "密码长度限制在8-32位，必须包含大小写字母、数字、特殊字符中的至少三种。",
					"modified":             "2026-07-02 10:00:00",
				},
			}},
		})
	}))
	defer tapdAPI.Close()
	t.Setenv("TAPD_API_BASE_URL", tapdAPI.URL)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/external-credential-profiles", map[string]any{
		"provider":   "tapd",
		"name":       fmt.Sprintf("tapd-auto-fetch-url-%d", time.Now().UnixNano()),
		"secret_ref": "env:TAPD_AUTO_FETCH_URL_TEST_TOKEN",
	})
	testHandler.CreateExternalCredentialProfile(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateExternalCredentialProfile: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	runtimeID := createClaimReclaimRuntime(t, ctx, "Source fetch URL parse runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Source fetch URL parse agent")
	taskID := createDispatchedClaimFixtureTask(t, ctx, agentID, runtimeID, issueID, "1 second", true)
	rawMetadata, _ := json.Marshal(map[string]any{
		"source_url": "https://www.tapd.cn/47654106/markdown_wikis/show/\n  #1147654106001004223",
	})
	if _, err := testPool.Exec(ctx, `UPDATE issue SET metadata = $2 WHERE id = $1`, issueID, rawMetadata); err != nil {
		t.Fatalf("set issue metadata: %v", err)
	}

	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/issues/"+issueID+"/source-fetch", map[string]any{
		"provider":   "tapd",
		"auto_fetch": true,
	})
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	req = withURLParam(req, "id", issueID)
	testHandler.RecordIssueSourceFetch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("RecordIssueSourceFetch auto_fetch: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !sawAuth {
		t.Fatal("TAPD auto_fetch did not send credential-backed MCP authorization headers")
	}
	if sawWorkspaceID != "47654106" || sawWikiID != "1147654106001004223" {
		t.Fatalf("TAPD request query workspace_id=%q id=%q", sawWorkspaceID, sawWikiID)
	}
	var resp struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Metadata["source_fetch_status"] != "fetched" ||
		resp.Metadata["source_fetch_resource_type"] != "markdown_wiki" ||
		resp.Metadata["source_fetch_resource_id"] != "1147654106001004223" ||
		resp.Metadata["source_fetch_url"] != "https://www.tapd.cn/47654106/markdown_wikis/show/#1147654106001004223" {
		t.Fatalf("metadata = %+v", resp.Metadata)
	}
}

func TestRecordIssueSourceFetchAutoFetchRecordsMissingTapdProfile(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `DELETE FROM external_credential_profile WHERE user_id = $1 AND provider = 'tapd'`, testUserID); err != nil {
		t.Fatalf("clear tapd profiles: %v", err)
	}
	runtimeID := createClaimReclaimRuntime(t, ctx, "Source fetch missing profile runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Source fetch missing profile agent")
	taskID := createDispatchedClaimFixtureTask(t, ctx, agentID, runtimeID, issueID, "1 second", true)
	rawMetadata, _ := json.Marshal(map[string]any{
		"tapd_workspace_id":  "47654106",
		"tapd_resource_type": "markdown_wiki",
		"tapd_resource_id":   "1147654106001004154",
	})
	if _, err := testPool.Exec(ctx, `UPDATE issue SET metadata = $2 WHERE id = $1`, issueID, rawMetadata); err != nil {
		t.Fatalf("set issue metadata: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/source-fetch", map[string]any{
		"provider":   "tapd",
		"auto_fetch": true,
	})
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	req = withURLParam(req, "id", issueID)
	testHandler.RecordIssueSourceFetch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("RecordIssueSourceFetch auto_fetch missing profile: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Metadata["source_fetch_status"] != "fetch_failed" {
		t.Fatalf("metadata = %+v", resp.Metadata)
	}
	if !strings.Contains(fmt.Sprint(resp.Metadata["source_fetch_error"]), "no account-level TAPD credential profile") {
		t.Fatalf("source_fetch_error = %v", resp.Metadata["source_fetch_error"])
	}
}
