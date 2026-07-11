package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func createSquadWithKey(t *testing.T, key string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/squads", body), "workspaceId", testWorkspaceID)
	req.Header.Set("Idempotency-Key", key)
	testHandler.CreateSquad(w, req)
	return w
}

func cleanupSquadCreateRequest(t *testing.T, key, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM squad WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name)
		_, _ = testPool.Exec(ctx, `DELETE FROM squad_create_request WHERE workspace_id = $1 AND idempotency_key = $2`, testWorkspaceID, key)
	})
}

func TestCreateSquad_IdempotentReplayConflictAndCascade(t *testing.T) {
	key := uuid.NewString()
	leaderID := createHandlerTestAgent(t, "squad idempotent leader "+uuid.NewString(), []byte(`[]`))
	memberID := createHandlerTestAgent(t, "squad idempotent member "+uuid.NewString(), []byte(`[]`))
	name := "idempotent squad " + uuid.NewString()
	cleanupSquadCreateRequest(t, key, name)
	body := map[string]any{
		"name": name, "leader_id": leaderID, "scope": "personal",
		"members": []map[string]any{{"member_type": "agent", "member_id": memberID}},
	}

	first := createSquadWithKey(t, key, body)
	replay := createSquadWithKey(t, key, body)
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated {
		t.Fatalf("first/replay = %d/%d: %s / %s", first.Code, replay.Code, first.Body.String(), replay.Body.String())
	}
	if first.Body.String() != replay.Body.String() {
		t.Fatalf("replay body differs\nfirst:  %s\nreplay: %s", first.Body, replay.Body)
	}
	var squad SquadResponse
	if err := json.Unmarshal(first.Body.Bytes(), &squad); err != nil {
		t.Fatalf("decode squad: %v", err)
	}
	if squad.MemberCount != 2 {
		t.Fatalf("member_count = %d, want 2", squad.MemberCount)
	}

	conflict := createSquadWithKey(t, key, map[string]any{
		"name": name + " changed", "leader_id": leaderID, "scope": "personal",
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed replay = %d %s, want 409", conflict.Code, conflict.Body.String())
	}
	if conflict.Body.String() != "{\"code\":\"idempotency_conflict\",\"error\":\"Idempotency-Key was already used with a different request\"}\n" {
		t.Fatalf("conflict body = %s", conflict.Body.String())
	}

	var squads, members, requests int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM squad WHERE id = $1`, squad.ID).Scan(&squads)
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM squad_member WHERE squad_id = $1`, squad.ID).Scan(&members)
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM squad_create_request WHERE workspace_id = $1 AND idempotency_key = $2`, testWorkspaceID, key).Scan(&requests)
	if squads != 1 || members != 2 || requests != 1 {
		t.Fatalf("squads=%d members=%d requests=%d, want 1/2/1", squads, members, requests)
	}
	if _, err := testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squad.ID); err != nil {
		t.Fatalf("delete squad: %v", err)
	}
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM squad_create_request WHERE workspace_id = $1 AND idempotency_key = $2`, testWorkspaceID, key).Scan(&requests)
	if requests != 0 {
		t.Fatalf("request rows after squad delete = %d, want 0", requests)
	}
}

func TestCreateSquad_ConcurrentReplayCreatesOneSquad(t *testing.T) {
	key := uuid.NewString()
	leaderID := createHandlerTestAgent(t, "squad concurrent leader "+uuid.NewString(), []byte(`[]`))
	name := "concurrent squad " + uuid.NewString()
	cleanupSquadCreateRequest(t, key, name)
	body := map[string]any{"name": name, "leader_id": leaderID, "scope": "personal"}

	const callers = 10
	responses := make(chan *httptest.ResponseRecorder, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- createSquadWithKey(t, key, body)
		}()
	}
	wg.Wait()
	close(responses)

	ids := map[string]struct{}{}
	for response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("concurrent create = %d %s", response.Code, response.Body.String())
		}
		var squad SquadResponse
		if err := json.Unmarshal(response.Body.Bytes(), &squad); err != nil {
			t.Fatalf("decode concurrent response: %v", err)
		}
		ids[squad.ID] = struct{}{}
	}
	if len(ids) != 1 {
		t.Fatalf("concurrent responses returned %d squad ids: %v", len(ids), ids)
	}
	var squads int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM squad WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name).Scan(&squads)
	if squads != 1 {
		t.Fatalf("squad rows = %d, want 1", squads)
	}
}

func TestCreateSquad_FailedResponseCompletionRollsBackEverything(t *testing.T) {
	key := uuid.NewString()
	leaderID := createHandlerTestAgent(t, "squad completion leader "+uuid.NewString(), []byte(`[]`))
	name := "forced squad completion " + uuid.NewString()
	cleanupSquadCreateRequest(t, key, name)
	suffix := uuid.NewString()
	functionName := quoteIdentifier("fail_squad_create_completion_" + suffix)
	triggerName := quoteIdentifier("fail_squad_create_completion_trigger_" + suffix)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.idempotency_key = %s::uuid AND NEW.response_body IS NOT NULL THEN
				RAISE EXCEPTION 'forced squad request completion failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON squad_create_request
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, quoteSQLLiteral(key), triggerName, functionName)); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON squad_create_request`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})

	response := createSquadWithKey(t, key, map[string]any{
		"name": name, "leader_id": leaderID, "scope": "personal",
	})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("forced failure = %d %s, want 500", response.Code, response.Body.String())
	}
	var squads, members, requests int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM squad WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name).Scan(&squads)
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM squad_member sm JOIN squad s ON s.id = sm.squad_id WHERE s.workspace_id = $1 AND s.name = $2`, testWorkspaceID, name).Scan(&members)
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM squad_create_request WHERE workspace_id = $1 AND idempotency_key = $2`, testWorkspaceID, key).Scan(&requests)
	if squads != 0 || members != 0 || requests != 0 {
		t.Fatalf("failed create left squads=%d members=%d requests=%d", squads, members, requests)
	}
}
