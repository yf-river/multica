package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestUpdateMemberPreservesTargetReadFailure(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      parseUUID(testUserID),
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load member fixture: %v", err)
	}

	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetMember"})
	memberID := uuidToString(member.ID)
	req := newRequest(http.MethodPatch, "/api/workspaces/"+testWorkspaceID+"/members/"+memberID, map[string]any{
		"role": member.Role,
	})
	req = withURLParams(req, "id", testWorkspaceID, "memberId", memberID)
	w := httptest.NewRecorder()

	h.UpdateMember(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("member target query failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
