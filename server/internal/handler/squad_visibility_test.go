package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSquadPersonalVisibility_IsCreatorOnlyForPlainMembers(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	_, _, otherMemberID := personalAgentTestFixture(t)
	leaderID := createHandlerTestAgent(t, "Personal Squad Visibility Leader", nil)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, scope)
		VALUES ($1, 'Personal Scope Test', '', $2, $3, 'personal')
		RETURNING id
	`, testWorkspaceID, leaderID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create personal squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	listW := httptest.NewRecorder()
	listReq := withURLParam(newRequestAs(otherMemberID, http.MethodGet, "/api/squads", nil), "workspaceId", testWorkspaceID)
	testHandler.ListSquads(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("ListSquads: expected 200, got %d: %s", listW.Code, listW.Body.String())
	}
	var squads []SquadResponse
	if err := json.NewDecoder(listW.Body).Decode(&squads); err != nil {
		t.Fatalf("decode squads: %v", err)
	}
	for _, squad := range squads {
		if squad.ID == squadID {
			t.Fatalf("plain member saw someone else's personal squad in list")
		}
	}

	getW := httptest.NewRecorder()
	getReq := withURLParams(
		newRequestAs(otherMemberID, http.MethodGet, "/api/squads/"+squadID, nil),
		"workspaceId", testWorkspaceID,
		"id", squadID,
	)
	testHandler.GetSquad(getW, getReq)
	if getW.Code != http.StatusNotFound {
		t.Fatalf("GetSquad: expected 404, got %d: %s", getW.Code, getW.Body.String())
	}

	assignW := httptest.NewRecorder()
	assignReq := newRequestAs(otherMemberID, http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "Cannot use someone else's personal squad",
		"assignee_type": "squad",
		"assignee_id":   squadID,
	})
	testHandler.CreateIssue(assignW, assignReq)
	if assignW.Code != http.StatusForbidden {
		t.Fatalf("CreateIssue personal squad: expected 403, got %d: %s", assignW.Code, assignW.Body.String())
	}
}

func TestCreateSquad_MemberCanCreatePersonalSquad(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	leaderID, memberID, _ := personalAgentTestFixture(t)

	w := httptest.NewRecorder()
	req := withURLParam(newRequestAs(memberID, http.MethodPost, "/api/squads", map[string]any{
		"name":        "Member Personal Squad",
		"leader_id":   leaderID,
		"scope":       "personal",
		"description": "created by plain member",
	}), "workspaceId", testWorkspaceID)
	testHandler.CreateSquad(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateSquad: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created SquadResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode created squad: %v", err)
	}
	if created.Scope != "personal" {
		t.Fatalf("created scope = %q, want personal", created.Scope)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, created.ID)
	})
}
