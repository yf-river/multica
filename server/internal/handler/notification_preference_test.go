package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/requestctx"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestGetNotificationPreferencesRejectsCorruptedStoredShape(t *testing.T) {
	ctx := context.Background()
	mustExec(t, ctx, `
		INSERT INTO notification_preference (workspace_id, user_id, preferences)
		VALUES ($1, $2, '[]'::jsonb)
		ON CONFLICT (workspace_id, user_id)
		DO UPDATE SET preferences = EXCLUDED.preferences
	`, testWorkspaceID, testUserID)
	t.Cleanup(func() {
		mustExec(t, context.Background(), `
			DELETE FROM notification_preference
			WHERE workspace_id = $1 AND user_id = $2
		`, testWorkspaceID, testUserID)
	})

	req := newRequest(http.MethodGet, "/api/notification-preferences", nil)
	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		UserID:      util.MustParseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("load member context: %v", err)
	}
	req = req.WithContext(requestctx.WithWorkspace(req.Context(), testWorkspaceID, member))
	w := httptest.NewRecorder()
	testHandler.GetNotificationPreferences(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected corrupt persisted preferences to fail with 500, got %d: %s", w.Code, w.Body.String())
	}
}
