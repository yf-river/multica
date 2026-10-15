package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestProjectResourcesForClaimIncludesGongfengRepository(t *testing.T) {
	config, err := normalizeRepositoryConfig(map[string]any{"url": "https://git.code.tencent.com/acme/service/-/tree/release", "provider": "gongfeng", "ref": "release"})
	if err != nil {
		t.Fatal(err)
	}
	resources, repos := projectResourcesForClaim([]db.ProjectResource{{
		ID:           pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		ResourceType: "gongfeng_repo",
		ResourceRef:  config,
	}})

	if len(resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(resources))
	}
	if len(repos) != 1 {
		t.Fatalf("repos = %d, want 1", len(repos))
	}
	if repos[0].URL != "https://git.code.tencent.com/acme/service.git" || repos[0].Ref != "release" {
		t.Fatalf("repo = %+v", repos[0])
	}
}
