package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// validateProjectStatus must accept the five DB-backed statuses and reject
// anything else with a message that lists the valid values. `project create`,
// `project update`, and `project status` all share it (#3925: `--status active`
// used to reach the server and 500 on the CHECK constraint).
func TestValidateProjectStatus(t *testing.T) {
	for _, s := range validProjectStatuses {
		if err := validateProjectStatus(s); err != nil {
			t.Errorf("status %q should be valid, got: %v", s, err)
		}
	}
	err := validateProjectStatus("active")
	if err == nil {
		t.Fatal("status \"active\" should be rejected")
	}
	if !strings.Contains(err.Error(), "planned") {
		t.Errorf("error should list valid statuses, got: %v", err)
	}
}

// newProjectResourceUpdateTestCmd mirrors the flag surface of
// projectResourceUpdateCmd so unit tests can exercise the shortcut-flag plumbing
// without spinning up a server.
func newProjectResourceUpdateTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "update"}
	c.Flags().String("url", "", "")
	c.Flags().String("default-branch-hint", "", "")
	c.Flags().String("local-path", "", "")
	c.Flags().String("daemon-id", "", "")
	c.Flags().String("ref-label", "", "")
	c.Flags().String("ref", "", "")
	c.Flags().String("label", "", "")
	c.Flags().Bool("clear-label", false, "")
	c.Flags().Int32("position", 0, "")
	c.Flags().String("output", "json", "")
	return c
}

// TestBuildResourceRefFromFlagsGithubMergesHint pins the nit fix from MUL-2662
// review round 2: `multica project resource update <p> <r> --default-branch-hint x`
// must rebuild the full github_repo payload by merging the existing `url` -
// otherwise the server sees `{default_branch_hint: "x"}` and 400s.
func TestBuildResourceRefFromFlagsGithubMergesHint(t *testing.T) {
	t.Run("hint-only edit preserves existing url", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("default-branch-hint", "main")
		existing := map[string]any{"url": "https://github.com/multica-ai/multica"}

		ref, has, err := buildResourceRefFromFlags(cmd, "github_repo", existing)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Fatalf("expected has=true when default-branch-hint is set")
		}
		if ref["url"] != "https://github.com/multica-ai/multica" {
			t.Errorf("expected merged url, got %v", ref["url"])
		}
		if ref["default_branch_hint"] != "main" {
			t.Errorf("expected merged hint=main, got %v", ref["default_branch_hint"])
		}
	})

	t.Run("hint=empty clears the hint but keeps url", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("default-branch-hint", "")
		existing := map[string]any{
			"url":                 "https://github.com/multica-ai/multica",
			"default_branch_hint": "stale",
		}
		ref, has, err := buildResourceRefFromFlags(cmd, "github_repo", existing)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Fatalf("expected has=true")
		}
		if ref["url"] != "https://github.com/multica-ai/multica" {
			t.Errorf("expected url to survive empty-hint clear, got %v", ref["url"])
		}
		if _, ok := ref["default_branch_hint"]; ok {
			t.Errorf("expected default_branch_hint to be cleared, got %v", ref["default_branch_hint"])
		}
	})

	t.Run("url override survives merge", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("url", "https://github.com/multica-ai/new-repo")
		existing := map[string]any{
			"url":                 "https://github.com/multica-ai/multica",
			"default_branch_hint": "main",
		}
		ref, has, err := buildResourceRefFromFlags(cmd, "github_repo", existing)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Fatalf("expected has=true")
		}
		if ref["url"] != "https://github.com/multica-ai/new-repo" {
			t.Errorf("expected overridden url, got %v", ref["url"])
		}
		if ref["default_branch_hint"] != "main" {
			t.Errorf("expected merged hint to persist, got %v", ref["default_branch_hint"])
		}
	})

	t.Run("hint-only with no existing url fails fast", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("default-branch-hint", "main")
		_, _, err := buildResourceRefFromFlags(cmd, "github_repo", nil)
		if err == nil {
			t.Fatalf("expected error when no existing url is available to merge")
		}
	})

	t.Run("no flags set returns has=false", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		ref, has, err := buildResourceRefFromFlags(cmd, "github_repo", map[string]any{"url": "https://x"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if has {
			t.Errorf("expected has=false when no shortcut flag is set, got ref=%v", ref)
		}
	})
}

// TestBuildResourceRefFromFlagsLocalDirectoryMerges covers the same merge
// behavior for local_directory: partial edits keep unmentioned fields from the
// existing ref.
func TestBuildResourceRefFromFlagsLocalDirectoryMerges(t *testing.T) {
	t.Run("ref-label only edit preserves existing path + daemon", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("ref-label", "renamed")
		existing := map[string]any{
			"local_path": "/Users/foo/work/a",
			"daemon_id":  "d1",
			"label":      "old",
		}
		ref, has, err := buildResourceRefFromFlags(cmd, "local_directory", existing)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Fatalf("expected has=true")
		}
		if ref["local_path"] != "/Users/foo/work/a" {
			t.Errorf("local_path missing after merge: %v", ref["local_path"])
		}
		if ref["daemon_id"] != "d1" {
			t.Errorf("daemon_id missing after merge: %v", ref["daemon_id"])
		}
		if ref["label"] != "renamed" {
			t.Errorf("label not overridden: %v", ref["label"])
		}
	})

	t.Run("local-path only without existing daemon fails", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("local-path", "/Users/foo/work/b")
		_, _, err := buildResourceRefFromFlags(cmd, "local_directory", nil)
		if err == nil {
			t.Fatalf("expected error when daemon_id is missing from both flags and existing ref")
		}
	})

	t.Run("ref-label cleared on empty input", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("ref-label", "")
		existing := map[string]any{
			"local_path": "/Users/foo/work/a",
			"daemon_id":  "d1",
			"label":      "to-clear",
		}
		ref, has, err := buildResourceRefFromFlags(cmd, "local_directory", existing)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Fatalf("expected has=true")
		}
		if _, ok := ref["label"]; ok {
			t.Errorf("expected embedded label to be cleared, got %v", ref["label"])
		}
	})
}

func newProjectListTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("status", "", "")
	cmd.Flags().String("output", "json", "")
	cmd.Flags().Bool("full-id", false, "")
	return cmd
}

func TestRunProjectListJSONIncludesResourceSummaries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-123")

	srv := newProjectResourceListServer(t)
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := newProjectListTestCmd()
	_ = cmd.Flags().Set("output", "json")

	out, err := captureStdout(t, func() error {
		return runProjectList(cmd, nil)
	})
	if err != nil {
		t.Fatalf("runProjectList: %v", err)
	}

	var projects []map[string]any
	if err := json.Unmarshal([]byte(out), &projects); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if len(projects) != 1 {
		t.Fatalf("projects len = %d, want 1", len(projects))
	}
	resources, _ := projects[0]["resources"].([]any)
	if len(resources) != 1 {
		t.Fatalf("resources len = %d, want 1", len(resources))
	}
	summaries, _ := projects[0]["resource_summaries"].([]any)
	if len(summaries) != 1 {
		t.Fatalf("resource_summaries len = %d, want 1", len(summaries))
	}
	summary, _ := summaries[0].(map[string]any)
	if got := summary["project_path"]; got != "ChainWeaver/ida/ida-deployment" {
		t.Fatalf("project_path = %v, want ChainWeaver/ida/ida-deployment", got)
	}
}

func TestFetchProjectCandidatesIncludesResourceDetail(t *testing.T) {
	srv := newProjectResourceListServer(t)
	defer srv.Close()
	client := cli.NewAPIClient(srv.URL, "workspace-123", "test-token")

	candidates, err := fetchProjectCandidates(t.Context(), client)
	if err != nil {
		t.Fatalf("fetchProjectCandidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates len = %d, want 1", len(candidates))
	}
	if got := candidates[0].Detail; got != "active | ChainWeaver/ida/ida-deployment" {
		t.Fatalf("detail = %q, want active | ChainWeaver/ida/ida-deployment", got)
	}
}

func newProjectResourceListServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Workspace-ID") != "workspace-123" {
			t.Fatalf("X-Workspace-ID = %q, want workspace-123", r.Header.Get("X-Workspace-ID"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/projects":
			if got := r.URL.Query().Get("workspace_id"); got != "workspace-123" {
				t.Fatalf("workspace_id = %q, want workspace-123", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"projects": []map[string]any{
					{
						"id":             "project-deployment",
						"title":          "配置",
						"status":         "active",
						"resource_count": 1,
					},
				},
				"total": 1,
			})
		case "/api/projects/project-deployment/resources":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resources": []map[string]any{
					{
						"id":            "resource-1",
						"resource_type": "gongfeng_repo",
						"resource_ref": map[string]any{
							"provider":      "gongfeng",
							"project_path":  "ChainWeaver/ida/ida-deployment",
							"url":           "https://git.code.tencent.com/ChainWeaver/ida/ida-deployment",
							"branch":        "v5.0.0_dev",
							"ref":           "v5.0.0_dev",
							"resource_kind": "branch",
						},
					},
				},
				"total": 1,
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.String())
		}
	}))
}
