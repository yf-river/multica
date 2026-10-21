package handler

import (
	"github.com/multica-ai/multica/server/pkg/investigation"
	"testing"
)

func TestSupervisionSourcesShareCurrentDirectoryButNotArtifactScopes(t *testing.T) {
	roots := distinctSupervisionRoots([]investigation.Root{
		{ID: "run:new", Path: "/repo", RunID: "new"},
		{ID: "run:old", Path: "/repo/.", RunID: "old"},
		{ID: "resource:project", Path: "/repo"},
		{ID: "run:other", Path: "/other", RunID: "other"},
		{ID: "evidence:one", Path: "/repo/.run/evidence", ArtifactPrefix: "GONG-1"},
		{ID: "evidence:two", Path: "/repo/.run/evidence", ArtifactPrefix: "GONG-2"},
	})
	if len(roots) != 4 || roots[0].ID != "resource:project" || roots[0].RunID != "" || roots[1].ID != "run:other" {
		t.Fatalf("wrong source identity: %+v", roots)
	}
	if roots[2].ArtifactPrefix == roots[3].ArtifactPrefix {
		t.Fatal("artifact scopes merged")
	}
}
