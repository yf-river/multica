package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func createProjectWithKey(
	t *testing.T,
	key string,
	body map[string]any,
) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/projects?workspace_id="+testWorkspaceID, body)
	req.Header.Set("Idempotency-Key", key)
	testHandler.CreateProject(w, req)
	return w
}

func cleanupProjectCreateRequest(t *testing.T, key, title string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM project WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title)
		_, _ = testPool.Exec(ctx, `DELETE FROM project_create_request WHERE workspace_id = $1 AND idempotency_key = $2`, testWorkspaceID, key)
	})
}

func TestCreateProject_IdempotentReplayAndConflict(t *testing.T) {
	key := uuid.NewString()
	title := "idempotent project " + uuid.NewString()
	cleanupProjectCreateRequest(t, key, title)
	body := map[string]any{"title": title, "status": "planned"}

	first := createProjectWithKey(t, key, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d %s", first.Code, first.Body.String())
	}
	replay := createProjectWithKey(t, key, body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body differs\nfirst:  %s\nreplay: %s", first.Body, replay.Body)
	}

	var project CreateProjectResponse
	if err := json.Unmarshal(first.Body.Bytes(), &project); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	var projects, requests int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM project WHERE id = $1`, project.ID).Scan(&projects); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM project_create_request WHERE workspace_id = $1 AND idempotency_key = $2`, testWorkspaceID, key).Scan(&requests); err != nil {
		t.Fatalf("count requests: %v", err)
	}
	if projects != 1 || requests != 1 {
		t.Fatalf("projects=%d requests=%d, want 1/1", projects, requests)
	}

	conflict := createProjectWithKey(t, key, map[string]any{"title": title + " changed"})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed replay = %d %s, want 409", conflict.Code, conflict.Body.String())
	}
	if body := conflict.Body.String(); body != "{\"code\":\"idempotency_conflict\",\"error\":\"Idempotency-Key was already used with a different request\"}\n" {
		t.Fatalf("conflict body = %s", body)
	}
	if _, err := testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, project.ID); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM project_create_request WHERE workspace_id = $1 AND idempotency_key = $2`, testWorkspaceID, key).Scan(&requests); err != nil {
		t.Fatalf("count requests after project delete: %v", err)
	}
	if requests != 0 {
		t.Fatalf("request rows after project delete = %d, want 0", requests)
	}
}

func TestCreateProject_ConcurrentReplayCreatesOneProject(t *testing.T) {
	key := uuid.NewString()
	title := "concurrent project " + uuid.NewString()
	cleanupProjectCreateRequest(t, key, title)
	body := map[string]any{"title": title, "priority": "high"}

	const callers = 10
	responses := make(chan *httptest.ResponseRecorder, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- createProjectWithKey(t, key, body)
		}()
	}
	wg.Wait()
	close(responses)

	ids := map[string]struct{}{}
	for response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("concurrent create = %d %s", response.Code, response.Body.String())
		}
		var project CreateProjectResponse
		if err := json.Unmarshal(response.Body.Bytes(), &project); err != nil {
			t.Fatalf("decode concurrent response: %v", err)
		}
		ids[project.ID] = struct{}{}
	}
	if len(ids) != 1 {
		t.Fatalf("concurrent responses returned %d project ids: %v", len(ids), ids)
	}
	var projects int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM project WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title).Scan(&projects); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if projects != 1 {
		t.Fatalf("project rows = %d, want 1", projects)
	}
}

func TestCreateProject_ReplaysBundledResourceResponse(t *testing.T) {
	key := uuid.NewString()
	title := "resource replay project " + uuid.NewString()
	cleanupProjectCreateRequest(t, key, title)
	body := map[string]any{
		"title": title,
		"resources": []map[string]any{{
			"resource_type": "github_repo",
			"resource_ref":  map[string]any{"url": "https://github.com/example/" + uuid.NewString()},
		}},
	}

	first := createProjectWithKey(t, key, body)
	replay := createProjectWithKey(t, key, body)
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated {
		t.Fatalf("first/replay = %d/%d: %s / %s", first.Code, replay.Code, first.Body.String(), replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("resource replay body differs\nfirst:  %s\nreplay: %s", first.Body, replay.Body)
	}
	var response CreateProjectResponse
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Resources) != 1 || response.ResourceCount != 1 {
		t.Fatalf("resources=%d resource_count=%d, want 1/1", len(response.Resources), response.ResourceCount)
	}
	var resources int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM project_resource WHERE project_id = $1`, response.ID).Scan(&resources); err != nil {
		t.Fatalf("count resources: %v", err)
	}
	if resources != 1 {
		t.Fatalf("resource rows = %d, want 1", resources)
	}
}

func TestCreateProject_FailedResponseCompletionRollsBackProject(t *testing.T) {
	key := uuid.NewString()
	title := "forced project failure " + uuid.NewString()
	cleanupProjectCreateRequest(t, key, title)
	suffix := uuid.NewString()
	functionName := `fail_project_create_completion_` + suffix
	triggerName := `fail_project_create_completion_trigger_` + suffix
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.idempotency_key = %s::uuid AND NEW.response_body IS NOT NULL THEN
				RAISE EXCEPTION 'forced project request completion failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON project_create_request
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, quoteIdentifier(functionName), quoteSQLLiteral(key), quoteIdentifier(triggerName), quoteIdentifier(functionName))); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON project_create_request`, quoteIdentifier(triggerName)))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, quoteIdentifier(functionName)))
	})

	response := createProjectWithKey(t, key, map[string]any{"title": title})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("forced failure = %d %s, want 500", response.Code, response.Body.String())
	}
	var projects, requests int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM project WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title).Scan(&projects)
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM project_create_request WHERE workspace_id = $1 AND idempotency_key = $2`, testWorkspaceID, key).Scan(&requests)
	if projects != 0 || requests != 0 {
		t.Fatalf("failed create left projects=%d requests=%d", projects, requests)
	}
}

func quoteIdentifier(value string) string {
	return `"` + value + `"`
}

func quoteSQLLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}
