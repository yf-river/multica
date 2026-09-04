package handler

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestProjectResourcesForClaimIncludesGongfengRepository(t *testing.T) {
	resources, repos := projectResourcesForClaim([]db.ProjectResource{{
		ID:           pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		ResourceType: "gongfeng_repo",
		ResourceRef:  json.RawMessage(`{"url":"https://git.code.tencent.com/acme/service/-/tree/release","project_path":"acme/service","ref":"release"}`),
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
