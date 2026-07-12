package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCreateSquadRejectsNonObjectSOPProfile(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	leaderID, memberID, _ := personalAgentTestFixture(t)
	for i, profile := range []any{nil, []any{"stage"}, "profile"} {
		name := fmt.Sprintf("invalid-sop-profile-%d-%d", time.Now().UnixNano(), i)
		t.Cleanup(func() {
			mustExec(t, context.Background(), `DELETE FROM squad WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name)
		})
		w := httptest.NewRecorder()
		req := withURLParam(newRequestAs(memberID, http.MethodPost, "/api/squads", map[string]any{
			"name": name, "leader_id": leaderID, "scope": "personal", "sop_profile": profile,
		}), "workspaceId", testWorkspaceID)
		testHandler.CreateSquad(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("sop_profile=%#v: expected 400, got %d: %s", profile, w.Code, w.Body.String())
		}
	}
}

func TestDecodeSquadSOPProfileRejectsInvalidPersistence(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`null`), []byte(`[]`), []byte(`"profile"`)} {
		if _, err := decodeSquadSOPProfile(raw); err == nil {
			t.Fatalf("sop_profile=%s expected an error", raw)
		}
	}
	profile, err := decodeSquadSOPProfile([]byte(`{"mode":"stage_chain"}`))
	if err != nil {
		t.Fatalf("decode object profile: %v", err)
	}
	if profile["mode"] != "stage_chain" {
		t.Fatalf("decoded profile = %#v", profile)
	}
}

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
		mustExec(t, context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
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
		mustExec(t, context.Background(), `DELETE FROM squad WHERE id = $1`, created.ID)
	})
}

func TestUpdateSquadValidationFailureDoesNotAddLeaderMembership(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	personalLeaderID := createHandlerTestAgent(t, "Atomic Squad Personal Leader", nil)
	workspaceLeaderID := createHandlerTestAgent(t, "Atomic Squad Workspace Leader", nil)
	mustExec(t, context.Background(), `UPDATE agent SET scope = 'workspace', owner_id = NULL WHERE id = $1`, workspaceLeaderID)

	createW := httptest.NewRecorder()
	createReq := withURLParam(newRequest(http.MethodPost, "/api/squads", map[string]any{
		"name": "Atomic Squad Update", "leader_id": personalLeaderID, "scope": "personal",
	}), "workspaceId", testWorkspaceID)
	testHandler.CreateSquad(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create squad: %d %s", createW.Code, createW.Body.String())
	}
	var squad SquadResponse
	if err := json.NewDecoder(createW.Body).Decode(&squad); err != nil {
		t.Fatalf("decode squad: %v", err)
	}
	t.Cleanup(func() { mustExec(t, context.Background(), `DELETE FROM squad WHERE id = $1`, squad.ID) })

	updateW := httptest.NewRecorder()
	updateReq := withURLParams(newRequest(http.MethodPatch, "/api/squads/"+squad.ID, map[string]any{
		"leader_id": workspaceLeaderID,
	}), "workspaceId", testWorkspaceID, "id", squad.ID)
	testHandler.UpdateSquad(updateW, updateReq)
	if updateW.Code != http.StatusBadRequest {
		t.Fatalf("update squad: expected 400, got %d: %s", updateW.Code, updateW.Body.String())
	}
	var membershipCount int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*)::int FROM squad_member WHERE squad_id = $1 AND member_type = 'agent' AND member_id = $2`, squad.ID, workspaceLeaderID).Scan(&membershipCount); err != nil {
		t.Fatalf("count rejected leader membership: %v", err)
	}
	if membershipCount != 0 {
		t.Fatalf("validation failure left %d rejected leader memberships, want 0", membershipCount)
	}
}
