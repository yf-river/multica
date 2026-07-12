package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestUpdateSquadMemberRolePreservesDatabaseFailure(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	leaderID := createHandlerTestAgent(t, "role-read-leader-"+randomID()[:8], []byte(`[]`))
	memberID := createHandlerTestAgent(t, "role-read-member-"+randomID()[:8], []byte(`[]`))
	createW := httptest.NewRecorder()
	createReq := newRequest(http.MethodPost, "/api/squads", map[string]any{
		"name":      "role read failure " + randomID()[:8],
		"leader_id": leaderID,
		"scope":     "personal",
		"members": []map[string]any{{
			"member_type": "agent",
			"member_id":   memberID,
			"role":        "member",
		}},
	})
	createReq = withURLParam(createReq, "workspaceId", testWorkspaceID)
	testHandler.CreateSquad(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create squad: expected 201, got %d: %s", createW.Code, createW.Body.String())
	}
	var squad SquadResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &squad); err != nil {
		t.Fatalf("decode squad: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squad.ID)
	})

	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "UpdateSquadMemberRole"})
	req := newRequest(http.MethodPatch, "/api/squads/"+squad.ID+"/members/role", map[string]any{
		"member_type": "agent",
		"member_id":   memberID,
		"role":        "reviewer",
	})
	req = withURLParams(req, "workspaceId", testWorkspaceID, "id", squad.ID)
	w := httptest.NewRecorder()

	h.UpdateSquadMemberRole(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("squad member role query failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
