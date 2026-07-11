package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestCreateSquad_CreatesInitialMembersAtomically(t *testing.T) {
	leaderID := createHandlerTestAgent(t, "squad initial leader "+uuid.NewString(), []byte(`[]`))
	memberID := createHandlerTestAgent(t, "squad initial member "+uuid.NewString(), []byte(`[]`))
	title := "squad initial members " + uuid.NewString()
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM squad WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, title)
	})

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/squads", map[string]any{
		"name":      title,
		"leader_id": leaderID,
		"scope":     "personal",
		"members": []map[string]any{{
			"member_type": "agent",
			"member_id":   memberID,
			"role":        "reviewer",
		}},
	}), "workspaceId", testWorkspaceID)
	testHandler.CreateSquad(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s, want 201", w.Code, w.Body.String())
	}
	var resp SquadResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.MemberCount != 2 || len(resp.MemberPreview) != 2 {
		t.Fatalf("member summary = count %d preview %#v, want leader and initial member", resp.MemberCount, resp.MemberPreview)
	}
	if resp.MemberPreview[0].MemberID != leaderID || resp.MemberPreview[0].Role != "leader" {
		t.Fatalf("leader preview = %#v", resp.MemberPreview[0])
	}
	if resp.MemberPreview[1].MemberID != memberID || resp.MemberPreview[1].Role != "reviewer" {
		t.Fatalf("initial member preview = %#v", resp.MemberPreview[1])
	}

	var memberCount int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM squad_member WHERE squad_id = $1`, resp.ID).Scan(&memberCount); err != nil {
		t.Fatalf("count members: %v", err)
	}
	if memberCount != 2 {
		t.Fatalf("persisted member count = %d, want 2", memberCount)
	}
}

func TestCreateSquad_LeaderMembershipFailureRollsBackSquad(t *testing.T) {
	leaderID := createHandlerTestAgent(
		t,
		"squad atomic leader "+uuid.NewString(),
		[]byte(`[]`),
	)
	title := "squad atomic failure " + uuid.NewString()
	suffix := uuid.NewString()
	functionName := quoteIdentifier("fail_squad_leader_" + suffix)
	triggerName := quoteIdentifier("fail_squad_leader_trigger_" + suffix)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF EXISTS (SELECT 1 FROM squad WHERE id = NEW.squad_id AND name = %s) THEN
				RAISE EXCEPTION 'forced squad leader membership failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE INSERT ON squad_member
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, quoteSQLLiteral(title), triggerName, functionName)); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON squad_member`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
		_, _ = testPool.Exec(ctx, `DELETE FROM squad WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, title)
	})

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/squads", map[string]any{
		"name":      title,
		"leader_id": leaderID,
		"scope":     "personal",
	}), "workspaceId", testWorkspaceID)
	testHandler.CreateSquad(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("create = %d %s, want 500", w.Code, w.Body.String())
	}
	var squads int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM squad WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, title).Scan(&squads); err != nil {
		t.Fatalf("count squads: %v", err)
	}
	if squads != 0 {
		t.Fatalf("failed leader membership left %d squad rows", squads)
	}
}

func TestCreateSquad_InitialMemberFailureRollsBackSquadAndLeader(t *testing.T) {
	leaderID := createHandlerTestAgent(t, "squad rollback leader "+uuid.NewString(), []byte(`[]`))
	memberID := createHandlerTestAgent(t, "squad rollback member "+uuid.NewString(), []byte(`[]`))
	title := "squad initial member failure " + uuid.NewString()
	suffix := uuid.NewString()
	functionName := quoteIdentifier("fail_squad_initial_member_" + suffix)
	triggerName := quoteIdentifier("fail_squad_initial_member_trigger_" + suffix)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.member_id = %s::uuid THEN
				RAISE EXCEPTION 'forced initial squad member failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE INSERT ON squad_member
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, quoteSQLLiteral(memberID), triggerName, functionName)); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON squad_member`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
		_, _ = testPool.Exec(ctx, `DELETE FROM squad WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, title)
	})

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/squads", map[string]any{
		"name":      title,
		"leader_id": leaderID,
		"scope":     "personal",
		"members": []map[string]any{{
			"member_type": "agent",
			"member_id":   memberID,
		}},
	}), "workspaceId", testWorkspaceID)
	testHandler.CreateSquad(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("create = %d %s, want 500", w.Code, w.Body.String())
	}
	var squads int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM squad WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, title).Scan(&squads); err != nil {
		t.Fatalf("count squads: %v", err)
	}
	if squads != 0 {
		t.Fatalf("failed initial membership left %d squad rows", squads)
	}
}
