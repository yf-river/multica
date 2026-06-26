package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

func newIssueSourceFetchTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "source-fetch"}
	c.Flags().String("provider", "tapd", "")
	c.Flags().String("fetch-provider", "", "")
	c.Flags().String("status", "", "")
	c.Flags().String("url", "", "")
	c.Flags().String("source-workspace-id", "", "")
	c.Flags().String("resource-type", "", "")
	c.Flags().String("resource-id", "", "")
	c.Flags().String("title", "", "")
	c.Flags().String("summary", "", "")
	c.Flags().String("body-excerpt", "", "")
	c.Flags().String("version", "", "")
	c.Flags().String("error", "", "")
	c.Flags().Int64("duration-ms", 0, "")
	c.Flags().String("output", "json", "")
	return c
}

func TestRunIssueSourceFetchPostsUnifiedRecord(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/"+testIssueUUID:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         testIssueUUID,
				"identifier": "MUL-1",
				"title":      "test issue",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/"+testIssueUUID+"/source-fetch":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatalf("decode posted body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"metadata": map[string]any{"source_fetch_status": "fetched"},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-1")

	cmd := newIssueSourceFetchTestCmd()
	_ = cmd.Flags().Set("status", "fetched")
	_ = cmd.Flags().Set("source-workspace-id", "47654106")
	_ = cmd.Flags().Set("resource-type", "markdown_wiki")
	_ = cmd.Flags().Set("resource-id", "1147654106001004154")
	_ = cmd.Flags().Set("title", "用户快捷入口需求")
	_ = cmd.Flags().Set("summary", "快捷入口属于当前登录用户。")
	_ = cmd.Flags().Set("body-excerpt", "快捷入口属于当前登录用户，不同用户之间互不影响。")
	_ = cmd.Flags().Set("version", "2026-06-18 07:39:03")
	_ = cmd.Flags().Set("duration-ms", "1234")

	if _, err := captureStdout(t, func() error {
		return runIssueSourceFetch(cmd, []string{testIssueUUID})
	}); err != nil {
		t.Fatalf("runIssueSourceFetch: %v", err)
	}
	if posted["provider"] != "tapd" {
		t.Fatalf("provider = %v", posted["provider"])
	}
	if posted["status"] != "fetched" {
		t.Fatalf("status = %v", posted["status"])
	}
	if posted["workspace_id"] != "47654106" {
		t.Fatalf("workspace_id = %v", posted["workspace_id"])
	}
	if posted["resource_id"] != "1147654106001004154" {
		t.Fatalf("resource_id = %v", posted["resource_id"])
	}
	if posted["title"] != "用户快捷入口需求" {
		t.Fatalf("title = %v", posted["title"])
	}
	if posted["body_excerpt"] == "" {
		t.Fatalf("body_excerpt = %v", posted["body_excerpt"])
	}
	if posted["duration_ms"].(float64) != 1234 {
		t.Fatalf("duration_ms = %v", posted["duration_ms"])
	}
}

func TestRunIssueSourceFetchRequiresStatus(t *testing.T) {
	cmd := newIssueSourceFetchTestCmd()
	if err := runIssueSourceFetch(cmd, []string{testIssueUUID}); err == nil {
		t.Fatal("runIssueSourceFetch returned nil without --status")
	}
}
