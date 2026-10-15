package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func repositoryReq(method, path string, body any, id string) *http.Request {
	r := withURLParam(newRequest(method, path, body), "id", testWorkspaceID)
	if id != "" {
		r = withURLParams(r, "repositoryId", id)
	}
	return r
}

func TestUnifiedRepositoryProjectAndTaskUseSameConfiguration(t *testing.T) {
	ctx := context.Background()
	repoID := dbfx.Insert(t, "repository", testutil.Cols{"workspace_id": testWorkspaceID, "identity": "github.com/test/unified", "config": `{"url":"https://github.com/test/unified","provider":"github","ref":"main"}`})
	projectID := dbfx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "unified repository test"})
	resourceID := dbfx.Insert(t, "project_resource", testutil.Cols{"workspace_id": testWorkspaceID, "project_id": projectID, "resource_type": "github_repo", "resource_ref": `{"repository_id":"` + repoID + `"}`})
	var center struct {
		Repos    []map[string]any    `json:"repos"`
		Projects []repositoryProject `json:"projects"`
	}
	testutil.Call(t, testHandler.GetWorkspaceRepositoryCenter, repositoryReq("GET", "/api/workspaces/"+testWorkspaceID+"/repos/details", nil, "")).Want(200).JSON(&center)
	found := false
	for _, repo := range center.Repos {
		if repo["id"] == repoID {
			found = true
		}
	}
	if !found {
		t.Fatal("project repository missing from registry")
	}
	claim, err := testHandler.resolveClaimProjectContext(ctx, parseUUID(projectID), parseUUID(testWorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	if len(claim.Repos) != 1 || claim.Repos[0].Ref != "main" {
		t.Fatalf("claim=%+v", claim)
	}
	var updated map[string]any
	testutil.Call(t, testHandler.UpdateRepository, repositoryReq("PATCH", "/api/workspaces/"+testWorkspaceID+"/repos/"+repoID, map[string]any{"ref": "personal-studio-upstream"}, repoID)).Want(200).JSON(&updated)
	claim, err = testHandler.resolveClaimProjectContext(ctx, parseUUID(projectID), parseUUID(testWorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	if claim.Repos[0].Ref != "personal-studio-upstream" {
		t.Fatalf("stale branch=%s", claim.Repos[0].Ref)
	}
	_, err = testHandler.Queries.UpdateProjectResource(ctx, db.UpdateProjectResourceParams{ID: parseUUID(resourceID), ResourceRef: json.RawMessage(`{"repository_id":"` + repoID + `","ref":"project-branch"}`)})
	if err != nil {
		t.Fatal(err)
	}
	claim, err = testHandler.resolveClaimProjectContext(ctx, parseUUID(projectID), parseUUID(testWorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	if claim.Repos[0].Ref != "project-branch" {
		t.Fatal("project override lost")
	}
	fallback, err := testHandler.resolveClaimProjectContext(ctx, pgtype.UUID{}, parseUUID(testWorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range fallback.Repos {
		if repo.URL == "https://github.com/test/unified" {
			t.Fatal("project-only repository leaked into workspace defaults")
		}
	}
	testutil.Call(t, testHandler.DeleteRepository, repositoryReq("DELETE", "/api/workspaces/"+testWorkspaceID+"/repos/"+repoID, nil, repoID)).Want(409)
	testutil.Call(t, testHandler.UpdateRepository, repositoryReq("PATCH", "/api/workspaces/"+testWorkspaceID+"/repos/"+repoID, map[string]any{"ref": "stale", "revision": 1}, repoID)).Want(409)
}

func TestUnifiedRepositoryBindingCreatesOneEntityAndRejectsOtherWorkspace(t *testing.T) {
	ctx := context.Background()
	ws := parseUUID(testWorkspaceID)
	dbfx.Cleanup(t, "DELETE FROM repository WHERE workspace_id=$1 AND identity=$2", testWorkspaceID, "github.com/test/inline-unified")
	first, err := bindRepository(ctx, testHandler.Queries, ws, "github_repo", []byte(`{"url":"git@github.com:Test/Inline-Unified.git","ref":"branch-a"}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := bindRepository(ctx, testHandler.Queries, ws, "github_repo", []byte(`{"url":"https://github.com/test/inline-unified","ref":"branch-b"}`))
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	_ = json.Unmarshal(first, &a)
	_ = json.Unmarshal(second, &b)
	if a["repository_id"] != b["repository_id"] || a["ref"] != "branch-a" || b["ref"] != "branch-b" {
		t.Fatalf("bindings=%s %s", first, second)
	}
	if _, ok := a["url"]; ok {
		t.Fatal("project binding duplicates URL")
	}
	other := dbfx.Workspace(t, "Other repo scope", "other-repo-scope")
	if _, err = bindRepository(ctx, testHandler.Queries, parseUUID(other), "github_repo", first); err == nil {
		t.Fatal("cross-workspace binding accepted")
	}
}

func TestUnifiedRepositoryVersionIncludesCheckoutRef(t *testing.T) {
	a := normalizeWorkspaceRepos([]RepoData{{URL: "https://github.com/test/repo", Ref: "main"}})
	b := normalizeWorkspaceRepos([]RepoData{{URL: "https://github.com/test/repo", Ref: "next"}})
	if a[0].Ref != "main" || workspaceReposVersion(a) == workspaceReposVersion(b) {
		t.Fatal("branch change not delivered to daemon")
	}
}

func testRepositoryBinding(t *testing.T, ws, url, ref string) string {
	t.Helper()
	raw, err := bindRepository(context.Background(), testHandler.Queries, parseUUID(ws), "github_repo", []byte(`{"url":"`+url+`","ref":"`+ref+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
