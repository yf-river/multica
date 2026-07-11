package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

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
