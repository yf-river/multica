package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	assertConcurrentCreateReplay(t, func() *httptest.ResponseRecorder {
		return createSkillWithKey(t, key, body)
	}, func(response *httptest.ResponseRecorder) string {
		var skill SkillWithFilesResponse
		if err := json.Unmarshal(response.Body.Bytes(), &skill); err != nil {
			t.Fatal(err)
		}
		if len(skill.Files) != 1 || skill.Files[0].Path != "guide.md" {
			t.Fatalf("replayed files=%v", skill.Files)
		}
		return skill.ID
	})

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
	installResourceCreateCompletionFailure(t, "skill", key)
	ctx := context.Background()

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
