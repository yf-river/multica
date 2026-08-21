package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newRepoRegistryTestCmd(serverURL string) *cobra.Command {
	cmd := &cobra.Command{Use: "repo-test"}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().StringArray("url", nil, "")
	cmd.Flags().String("description", "", "")
	cmd.Flags().String("output", "json", "")
	_ = cmd.Flags().Set("server-url", serverURL)
	_ = cmd.Flags().Set("workspace-id", "ws-1")
	return cmd
}

type repoRegistryTestServer struct {
	server     *httptest.Server
	patched    []workspaceRepo
	patchCount int
}

func newRepoRegistryTestServer(t *testing.T, initialRepos []workspaceRepo) *repoRegistryTestServer {
	t.Helper()
	fixture := &repoRegistryTestServer{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/workspaces/ws-1":
			json.NewEncoder(w).Encode(repoWorkspaceResponse{ID: "ws-1", Repos: initialRepos})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/workspaces/ws-1":
			fixture.patchCount++
			var body struct {
				Repos []workspaceRepo `json:"repos"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode patch body: %v", err)
			}
			fixture.patched = body.Repos
			json.NewEncoder(w).Encode(repoWorkspaceResponse{ID: "ws-1", Repos: body.Repos})
		default:
			http.NotFound(w, r)
		}
	}))
	return fixture
}

func (s *repoRegistryTestServer) close() {
	s.server.Close()
}

func (s *repoRegistryTestServer) url() string {
	return s.server.URL
}

func TestRunRepoAddAppendsAndDedupes(t *testing.T) {
	initialRepos := []workspaceRepo{{URL: "https://git.example.com/web.git"}}
	srv := newRepoRegistryTestServer(t, initialRepos)
	defer srv.close()

	cmd := newRepoRegistryTestCmd(srv.url())
	if err := cmd.Flags().Set("url", "https://git.example.com/web.git"); err != nil {
		t.Fatal(err)
	}
	err := runRepoAdd(cmd, []string{
		"https://git.example.com/api.git",
		"https://git.example.com/api.git",
	})
	if err != nil {
		t.Fatalf("runRepoAdd: %v", err)
	}
	if srv.patchCount != 1 {
		t.Fatalf("patchCount = %d, want 1", srv.patchCount)
	}
	if len(srv.patched) != 2 {
		t.Fatalf("patched repos = %+v, want 2 entries", srv.patched)
	}
	if srv.patched[0].URL != "https://git.example.com/web.git" || srv.patched[1].URL != "https://git.example.com/api.git" {
		t.Fatalf("unexpected patched repos: %+v", srv.patched)
	}
}

func TestRunRepoAddUpdatesDescriptionForExistingRepo(t *testing.T) {
	initialRepos := []workspaceRepo{{URL: "https://git.example.com/web.git", Description: "old"}}
	srv := newRepoRegistryTestServer(t, initialRepos)
	defer srv.close()

	cmd := newRepoRegistryTestCmd(srv.url())
	if err := cmd.Flags().Set("description", "new"); err != nil {
		t.Fatal(err)
	}
	if err := runRepoAdd(cmd, []string{"https://git.example.com/web.git"}); err != nil {
		t.Fatalf("runRepoAdd: %v", err)
	}
	if len(srv.patched) != 1 || srv.patched[0].Description != "new" {
		t.Fatalf("patched repos = %+v, want updated description", srv.patched)
	}
}

func TestRunRepoAddRejectsDescriptionForMultipleRepos(t *testing.T) {
	cmd := newRepoRegistryTestCmd("http://127.0.0.1:0")
	if err := cmd.Flags().Set("description", "shared"); err != nil {
		t.Fatal(err)
	}
	err := runRepoAdd(cmd, []string{"https://git.example.com/a.git", "https://git.example.com/b.git"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "--description") {
		t.Fatalf("error = %q, want description guidance", err)
	}
}

func TestRunRepoRemoveDeletesExistingRepos(t *testing.T) {
	initialRepos := []workspaceRepo{
		{URL: "https://git.example.com/web.git"},
		{URL: "https://git.example.com/api.git"},
		{URL: "https://git.example.com/mobile.git"},
	}
	srv := newRepoRegistryTestServer(t, initialRepos)
	defer srv.close()

	cmd := newRepoRegistryTestCmd(srv.url())
	if err := cmd.Flags().Set("url", "https://git.example.com/mobile.git"); err != nil {
		t.Fatal(err)
	}
	if err := runRepoRemove(cmd, []string{"https://git.example.com/web.git"}); err != nil {
		t.Fatalf("runRepoRemove: %v", err)
	}
	if len(srv.patched) != 1 || srv.patched[0].URL != "https://git.example.com/api.git" {
		t.Fatalf("patched repos = %+v, want only api repo", srv.patched)
	}
}

func TestRunRepoRemoveRejectsMissingRepoWithoutPatch(t *testing.T) {
	srv := newRepoRegistryTestServer(t, []workspaceRepo{{URL: "https://git.example.com/web.git"}})
	defer srv.close()

	cmd := newRepoRegistryTestCmd(srv.url())
	err := runRepoRemove(cmd, []string{"https://git.example.com/missing.git"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %q, want not found", err)
	}
	if srv.patchCount != 0 {
		t.Fatalf("patchCount = %d, want 0", srv.patchCount)
	}
}

func TestNormalizeRepoCheckoutURL_GongfengPageURL(t *testing.T) {
	repoURL, ref := normalizeRepoCheckoutURL("https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev")
	if repoURL != "https://git.code.tencent.com/ChainWeaver/ida/user-center.git" {
		t.Fatalf("repoURL = %q", repoURL)
	}
	if ref != "v5.0.0_dev" {
		t.Fatalf("ref = %q", ref)
	}
}

func TestNormalizeRepoCheckoutURL_GongfengProjectURL(t *testing.T) {
	repoURL, ref := normalizeRepoCheckoutURL("https://git.code.tencent.com/ChainWeaver/ida/user-center")
	if repoURL != "https://git.code.tencent.com/ChainWeaver/ida/user-center.git" {
		t.Fatalf("repoURL = %q", repoURL)
	}
	if ref != "" {
		t.Fatalf("ref = %q", ref)
	}
}

func TestNormalizeRepoCheckoutURL_NonGongfengPassthrough(t *testing.T) {
	const input = "https://github.com/example/repo.git"
	repoURL, ref := normalizeRepoCheckoutURL(input)
	if repoURL != input {
		t.Fatalf("repoURL = %q", repoURL)
	}
	if ref != "" {
		t.Fatalf("ref = %q", ref)
	}
}
