package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestProjectResourcesForClaimUsesCurrentResourceContract(t *testing.T) {
	resources, repos, ref, err := projectResourcesForClaim([]db.ProjectResource{
		{ResourceType: "github_repo", ResourceRef: []byte(`{"url":"https://github.com/multica-ai/multica","default_branch_hint":"main"}`)},
		{ResourceType: "local_directory", ResourceRef: []byte(`{"local_path":"/work/multica","daemon_id":"daemon-1"}`)},
	})
	if err != nil {
		t.Fatalf("projectResourcesForClaim: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("resources = %d, want 2", len(resources))
	}
	if len(repos) != 1 || repos[0].URL != "https://github.com/multica-ai/multica" {
		t.Fatalf("repos = %#v", repos)
	}
	if ref != "main" {
		t.Fatalf("repo ref = %q, want main", ref)
	}
}

func TestProjectResourcesForClaimRejectsPersistedContractViolations(t *testing.T) {
	tests := []db.ProjectResource{
		{ResourceType: "github_repo", ResourceRef: []byte(`[]`)},
		{ResourceType: "github_repo", ResourceRef: []byte(`{"url":""}`)},
		{ResourceType: "retired_repo", ResourceRef: []byte(`{"url":"https://example.com/repo"}`)},
	}
	for _, resource := range tests {
		if _, _, _, err := projectResourcesForClaim([]db.ProjectResource{resource}); err == nil {
			t.Fatalf("projectResourcesForClaim accepted %s %s", resource.ResourceType, resource.ResourceRef)
		}
	}
}
