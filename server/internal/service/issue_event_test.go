package service

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueCreatedProjectionUsesReplayableIssueShape(t *testing.T) {
	issueID := pgtype.UUID{Bytes: [16]byte{15: 1}, Valid: true}
	workspaceID := pgtype.UUID{Bytes: [16]byte{15: 2}, Valid: true}
	creatorID := pgtype.UUID{Bytes: [16]byte{15: 3}, Valid: true}
	projection := issueCreatedProjection(db.Issue{
		ID:          issueID,
		WorkspaceID: workspaceID,
		Title:       "Replayable issue",
		Status:      "todo",
		Priority:    "none",
		CreatorType: "member",
		CreatorID:   creatorID,
	})

	raw, err := json.Marshal(map[string]any{"issue": projection})
	if err != nil {
		t.Fatalf("marshal issue event: %v", err)
	}
	var decoded struct {
		Issue struct {
			ID          string `json:"id"`
			WorkspaceID string `json:"workspace_id"`
			Title       string `json:"title"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("replay issue event: %v", err)
	}
	if decoded.Issue.ID == "" || decoded.Issue.WorkspaceID == "" || decoded.Issue.Title != "Replayable issue" {
		t.Fatalf("projection cannot be replayed: %+v", decoded.Issue)
	}
}
