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

func createSkillWithKey(t *testing.T, key string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/skills?workspace_id="+testWorkspaceID, body)
	req.Header.Set("Idempotency-Key", key)
	testHandler.CreateSkill(w, req)
	return w
}

func cleanupSkillCreateRequest(t *testing.T, key, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM skill WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name)
		_, _ = testPool.Exec(ctx, `DELETE FROM resource_create_request WHERE workspace_id = $1 AND resource_type = 'skill' AND idempotency_key = $2`, testWorkspaceID, key)
	})
}

func TestCreateSkill_IdempotentReplayConflictAndConcurrentCreate(t *testing.T) {
	key := uuid.NewString()
	name := "idempotent skill " + uuid.NewString()
	cleanupSkillCreateRequest(t, key, name)
	body := map[string]any{
		"name":  name,
		"files": []map[string]any{{"path": "guide.md", "content": "current guide"}},
	}

	const callers = 8
	responses := make(chan *httptest.ResponseRecorder, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- createSkillWithKey(t, key, body)
		}()
	}
	wg.Wait()
	close(responses)

	ids := map[string]struct{}{}
	var firstBody string
	for response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("create = %d %s", response.Code, response.Body.String())
		}
		if firstBody == "" {
			firstBody = response.Body.String()
		} else if response.Body.String() != firstBody {
			t.Fatalf("replay body differs\nfirst: %s\nnext: %s", firstBody, response.Body.String())
		}
		var skill SkillWithFilesResponse
		if err := json.Unmarshal(response.Body.Bytes(), &skill); err != nil {
			t.Fatal(err)
		}
		ids[skill.ID] = struct{}{}
		if len(skill.Files) != 1 || skill.Files[0].Path != "guide.md" {
			t.Fatalf("replayed files=%v", skill.Files)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("responses returned %d skill ids: %v", len(ids), ids)
	}

	conflict := createSkillWithKey(t, key, map[string]any{"name": name + " changed"})
	if conflict.Code != http.StatusConflict || conflict.Body.String() != "{\"code\":\"idempotency_conflict\",\"error\":\"Idempotency-Key was already used with a different request\"}\n" {
		t.Fatalf("conflict = %d %s", conflict.Code, conflict.Body.String())
	}
	var skills, requests int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM skill WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name).Scan(&skills)
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM resource_create_request WHERE workspace_id = $1 AND resource_type = 'skill' AND idempotency_key = $2`, testWorkspaceID, key).Scan(&requests)
	if skills != 1 || requests != 1 {
		t.Fatalf("skills=%d requests=%d, want 1/1", skills, requests)
	}
}

func TestCreateSkill_ResponseCompletionFailureRollsBackFiles(t *testing.T) {
	key := uuid.NewString()
	name := "failed skill " + uuid.NewString()
	cleanupSkillCreateRequest(t, key, name)
	suffix := uuid.NewString()
	functionName := quoteIdentifier("fail_skill_create_completion_" + suffix)
	triggerName := quoteIdentifier("fail_skill_create_completion_trigger_" + suffix)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.resource_type = 'skill' AND NEW.idempotency_key = %s::uuid AND NEW.response_body IS NOT NULL THEN
				RAISE EXCEPTION 'forced skill request completion failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON resource_create_request
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, quoteSQLLiteral(key), triggerName, functionName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON resource_create_request`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})

	response := createSkillWithKey(t, key, map[string]any{
		"name":  name,
		"files": []map[string]any{{"path": "guide.md", "content": "must roll back"}},
	})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response = %d %s, want 500", response.Code, response.Body.String())
	}
	var skills, files, requests int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM skill WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, name).Scan(&skills)
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM skill_file sf JOIN skill s ON s.id = sf.skill_id WHERE s.workspace_id = $1 AND s.name = $2`, testWorkspaceID, name).Scan(&files)
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM resource_create_request WHERE workspace_id = $1 AND resource_type = 'skill' AND idempotency_key = $2`, testWorkspaceID, key).Scan(&requests)
	if skills != 0 || files != 0 || requests != 0 {
		t.Fatalf("failed create left skills=%d files=%d requests=%d", skills, files, requests)
	}
}
