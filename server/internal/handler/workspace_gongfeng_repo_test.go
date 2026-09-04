package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseGongfengRepositoryURL(t *testing.T) {
	projectPath, err := parseGongfengRepositoryURL("https://git.code.tencent.com/ChainWeaver/ida/user-center.git")
	if err != nil {
		t.Fatalf("parse Gongfeng repository: %v", err)
	}
	if projectPath != "ChainWeaver/ida/user-center" {
		t.Fatalf("project path = %q", projectPath)
	}
}

func TestValidateWorkspaceReposRequiresResolvedGongfengIdentity(t *testing.T) {
	_, err := validateAndNormalizeWorkspaceRepos([]map[string]any{{
		"url": "https://git.code.tencent.com/ChainWeaver/ida/user-center",
	}})
	if err == nil || !strings.Contains(err.Error(), "项目路径") {
		t.Fatalf("unresolved Gongfeng repository error = %v", err)
	}

	raw, err := validateAndNormalizeWorkspaceRepos([]map[string]any{{
		"url":            "https://git.code.tencent.com/ChainWeaver/ida/user-center",
		"provider":       "gongfeng",
		"project_path":   "ChainWeaver/ida/user-center",
		"default_branch": "master",
	}})
	if err != nil {
		t.Fatalf("resolved Gongfeng repository: %v", err)
	}
	var repos []workspaceRepoRef
	if err := json.Unmarshal(raw, &repos); err != nil {
		t.Fatalf("decode normalized repositories: %v", err)
	}
	if len(repos) != 1 || repos[0].Ref != "master" {
		t.Fatalf("normalized repositories = %+v", repos)
	}
}

func TestParseGongfengRepositoryURLRejectsOtherHosts(t *testing.T) {
	if _, err := parseGongfengRepositoryURL("https://example.com/ChainWeaver/ida"); err == nil {
		t.Fatal("expected a non-Gongfeng host to be rejected")
	}
}

func TestListGongfengBranchesOrdersRecentWorkFirst(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects/42/repository/branches" || r.URL.Query().Get("page") != "1" {
			t.Fatalf("unexpected branch request: %s", r.URL.String())
		}
		if r.Header.Get("PRIVATE-TOKEN") != "secret" {
			t.Fatalf("missing Gongfeng credential header")
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "old", "commit": map[string]any{"committed_date": "2026-01-01T00:00:00Z"}},
			{"name": "new", "commit": map[string]any{"committed_date": "2026-02-01T00:00:00Z"}},
		})
	}))
	defer api.Close()
	t.Setenv("GONGFENG_API_BASE", api.URL)

	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/ws/repos/probe", nil)
	branches, err := listGongfengBranches(req, "secret", "42")
	if err != nil {
		t.Fatalf("list Gongfeng branches: %v", err)
	}
	if len(branches) != 2 || branches[0] != "new" || branches[1] != "old" {
		t.Fatalf("branch order = %v", branches)
	}
}
