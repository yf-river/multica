package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func newSkillImportKey(t *testing.T) string {
	t.Helper()
	key := uuid.NewString()
	t.Cleanup(func() {
		if testPool != nil {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM skill_import_request WHERE idempotency_key = $1`, key)
		}
	})
	return key
}

func assertSkillImportReplay(t *testing.T, key string, request map[string]any, status int, first []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequestAsUser(testUserID, http.MethodPost, "/api/skills/import", request)
	req.Header.Set("Idempotency-Key", key)
	testHandler.ImportSkill(w, req)
	var got, want any
	gotErr := json.Unmarshal(w.Body.Bytes(), &got)
	wantErr := json.Unmarshal(first, &want)
	if w.Code != status || gotErr != nil || wantErr != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("replay = %d %s, want exact %d %s", w.Code, w.Body.String(), status, string(first))
	}
}

func TestWriteSkillImportDuplicateConflictIncludesExistingSkill(t *testing.T) {
	w := httptest.NewRecorder()
	writeSkillImportDuplicateConflict(w, ExistingSkillIdentity{ID: "skill-123", Name: "review-helper"})

	if w.Code != 409 {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "a skill with this name already exists" {
		t.Fatalf("error = %v", body["error"])
	}
	existing, ok := body["existing_skill"].(map[string]any)
	if !ok {
		t.Fatalf("existing_skill missing or wrong type: %#v", body["existing_skill"])
	}
	if existing["id"] != "skill-123" || existing["name"] != "review-helper" {
		t.Fatalf("existing_skill = %#v", existing)
	}
}

func withMockClawHubImport(t *testing.T, skillName string) string {
	t.Helper()
	slug := "review-helper"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/skills/" + slug:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"skill": map[string]any{
					"slug":        slug,
					"displayName": skillName,
					"summary":     "Imported test skill",
					"tags":        map[string]string{"latest": "1.0.0"},
				},
			})
		case "/api/v1/skills/" + slug + "/versions/1.0.0":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": map[string]any{
					"version": "1.0.0",
					"files": []map[string]any{
						{"path": "SKILL.md", "size": 16},
					},
				},
			})
		case "/api/v1/skills/" + slug + "/file":
			_, _ = w.Write([]byte("# Imported\n"))
		default:
			t.Fatalf("unexpected ClawHub path: %s", r.URL.String())
		}
	}))
	prev := clawHubAPIBase
	clawHubAPIBase = srv.URL + "/api/v1"
	t.Cleanup(func() {
		clawHubAPIBase = prev
		srv.Close()
	})
	return "https://clawhub.ai/acme/" + slug
}

func TestImportSkillOnConflictSkipReturnsStructuredResult(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test DB not configured")
	}
	namePrefix := "url-import-skip"
	skillName := namePrefix + "-" + t.Name()
	existingID := insertHandlerTestSkill(t, namePrefix, "# Existing")
	importURL := withMockClawHubImport(t, skillName)
	key := newSkillImportKey(t)

	w := httptest.NewRecorder()
	req := newRequestAsUser(testUserID, http.MethodPost, "/api/skills/import", map[string]any{
		"url":         importURL,
		"on_conflict": "skip",
	})
	req.Header.Set("Idempotency-Key", key)
	testHandler.ImportSkill(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var body SkillImportResult
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "skipped" {
		t.Fatalf("status = %q", body.Status)
	}
	if body.ExistingSkill == nil || body.ExistingSkill.ID != existingID || body.ExistingSkill.Name != skillName {
		t.Fatalf("existing_skill = %#v", body.ExistingSkill)
	}
	assertSkillImportReplay(t, key, map[string]any{"url": importURL, "on_conflict": "skip"}, http.StatusOK, w.Body.Bytes())
}

func TestImportSkillOnConflictRenameCreatesSuffixedSkill(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test DB not configured")
	}
	namePrefix := "url-import-rename"
	skillName := namePrefix + "-" + t.Name()
	mustExec(t, context.Background(), `DELETE FROM skill WHERE workspace_id = $1 AND name LIKE $2`, testWorkspaceID, skillName+"-%")
	insertHandlerTestSkill(t, namePrefix, "# Existing")
	importURL := withMockClawHubImport(t, skillName)
	key := newSkillImportKey(t)
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM skill WHERE workspace_id = $1 AND name LIKE $2`, testWorkspaceID, skillName+"-%")
	})

	w := httptest.NewRecorder()
	req := newRequestAsUser(testUserID, http.MethodPost, "/api/skills/import", map[string]any{
		"url":         importURL,
		"on_conflict": "rename",
	})
	req.Header.Set("Idempotency-Key", key)
	testHandler.ImportSkill(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
	var body SkillImportResult
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "created" || body.Skill == nil {
		t.Fatalf("body = %#v", body)
	}
	if body.Skill.Name != skillName+"-2" {
		t.Fatalf("created skill name = %q, want %q", body.Skill.Name, skillName+"-2")
	}
	assertSkillImportReplay(t, key, map[string]any{"url": importURL, "on_conflict": "rename"}, http.StatusCreated, w.Body.Bytes())
}

func TestImportSkillOnConflictOverwriteReplaysUpdatedSkill(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test DB not configured")
	}
	namePrefix := "url-import-overwrite"
	skillName := namePrefix + "-" + t.Name()
	skillID := insertHandlerTestSkill(t, namePrefix, "# Existing")
	importURL := withMockClawHubImport(t, skillName)
	key := newSkillImportKey(t)
	request := map[string]any{"url": importURL, "on_conflict": "overwrite"}
	w := httptest.NewRecorder()
	req := newRequestAsUser(testUserID, http.MethodPost, "/api/skills/import", request)
	req.Header.Set("Idempotency-Key", key)
	testHandler.ImportSkill(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("overwrite status = %d: %s", w.Code, w.Body.String())
	}
	assertSkillImportReplay(t, key, request, http.StatusOK, w.Body.Bytes())
	var content string
	if err := testPool.QueryRow(context.Background(), `SELECT content FROM skill WHERE id = $1`, skillID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != "# Imported\n" {
		t.Fatalf("overwritten content = %q", content)
	}
}

func TestImportSkillReplaysCommittedDefaultImport(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test DB not configured")
	}
	skillName := "url-import-replay-" + t.Name()
	importURL := withMockClawHubImport(t, skillName)
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM skill WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, skillName)
	})
	requestKey := newSkillImportKey(t)
	create := func() (int, SkillWithFilesResponse) {
		w := httptest.NewRecorder()
		req := newRequestAsUser(testUserID, http.MethodPost, "/api/skills/import", map[string]any{"url": importURL})
		req.Header.Set("Idempotency-Key", requestKey)
		testHandler.ImportSkill(w, req)
		var body SkillWithFilesResponse
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}

	firstStatus, first := create()
	secondStatus, replay := create()
	if firstStatus != http.StatusCreated || secondStatus != http.StatusCreated {
		t.Fatalf("import statuses = %d/%d, want 201/201", firstStatus, secondStatus)
	}
	if first.ID == "" || replay.ID != first.ID || replay.CreatedAt != first.CreatedAt {
		t.Fatalf("replay = id %s created %s, want id %s created %s", replay.ID, replay.CreatedAt, first.ID, first.CreatedAt)
	}
	changed := httptest.NewRecorder()
	changedReq := newRequestAsUser(testUserID, http.MethodPost, "/api/skills/import", map[string]any{"url": importURL + "?changed=1"})
	changedReq.Header.Set("Idempotency-Key", requestKey)
	testHandler.ImportSkill(changed, changedReq)
	if changed.Code != http.StatusConflict {
		t.Fatalf("changed request with reused key = %d: %s", changed.Code, changed.Body.String())
	}
	changedEnvelope := httptest.NewRecorder()
	changedEnvelopeReq := newRequestAsUser(testUserID, http.MethodPost, "/api/skills/import", map[string]any{
		"url": importURL, "on_conflict": "fail",
	})
	changedEnvelopeReq.Header.Set("Idempotency-Key", requestKey)
	testHandler.ImportSkill(changedEnvelope, changedEnvelopeReq)
	if changedEnvelope.Code != http.StatusConflict {
		t.Fatalf("changed response contract with reused key = %d: %s", changedEnvelope.Code, changedEnvelope.Body.String())
	}
}

func TestImportSkillConcurrentReplayCreatesOneSkill(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test DB not configured")
	}
	skillName := "url-import-concurrent-" + t.Name()
	importURL := withMockClawHubImport(t, skillName)
	key := newSkillImportKey(t)
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM skill WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, skillName)
	})

	const workers = 8
	type result struct {
		status int
		body   SkillWithFilesResponse
	}
	results := make(chan result, workers)
	var start sync.WaitGroup
	start.Add(1)
	var workersDone sync.WaitGroup
	workersDone.Add(workers)
	for range workers {
		go func() {
			defer workersDone.Done()
			start.Wait()
			w := httptest.NewRecorder()
			req := newRequestAsUser(testUserID, http.MethodPost, "/api/skills/import", map[string]any{"url": importURL})
			req.Header.Set("Idempotency-Key", key)
			testHandler.ImportSkill(w, req)
			var body SkillWithFilesResponse
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			results <- result{status: w.Code, body: body}
		}()
	}
	start.Done()
	workersDone.Wait()
	close(results)

	var skillID string
	for got := range results {
		if got.status != http.StatusCreated || got.body.ID == "" {
			t.Fatalf("concurrent import = %d %#v, want replayed 201 skill", got.status, got.body)
		}
		if skillID == "" {
			skillID = got.body.ID
		} else if got.body.ID != skillID {
			t.Fatalf("concurrent import IDs differ: %s and %s", skillID, got.body.ID)
		}
	}
	var skills, requests int
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM skill WHERE workspace_id = $1 AND name = $2),
			(SELECT count(*) FROM skill_import_request WHERE workspace_id = $1 AND idempotency_key = $3)
	`, testWorkspaceID, skillName, key).Scan(&skills, &requests); err != nil {
		t.Fatal(err)
	}
	if skills != 1 || requests != 1 {
		t.Fatalf("concurrent import left skills=%d requests=%d, want 1/1", skills, requests)
	}
}

func TestImportSkillConcurrentDifferentKeysPreserveConflictContract(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test DB not configured")
	}
	skillName := "url-import-name-race-" + t.Name()
	importURL := withMockClawHubImport(t, skillName)
	keys := []string{newSkillImportKey(t), newSkillImportKey(t)}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM skill WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, skillName)
	})

	statuses := make(chan int, len(keys))
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(len(keys))
	for _, key := range keys {
		go func() {
			defer done.Done()
			start.Wait()
			w := httptest.NewRecorder()
			req := newRequestAsUser(testUserID, http.MethodPost, "/api/skills/import", map[string]any{
				"url": importURL, "on_conflict": "fail",
			})
			req.Header.Set("Idempotency-Key", key)
			testHandler.ImportSkill(w, req)
			statuses <- w.Code
		}()
	}
	start.Done()
	done.Wait()
	close(statuses)

	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusCreated] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("different-key name race statuses = %#v, want one 201 and one 409", counts)
	}
	var skills int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM skill WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, skillName).Scan(&skills); err != nil {
		t.Fatal(err)
	}
	if skills != 1 {
		t.Fatalf("different-key name race left %d skills, want 1", skills)
	}
}

func TestImportSkillCompletionFailureRollsBackSkill(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test DB not configured")
	}
	skillName := "url-import-rollback-" + t.Name()
	importURL := withMockClawHubImport(t, skillName)
	key := newSkillImportKey(t)
	suffix := uuid.NewString()
	functionName := quoteIdentifier("fail_skill_import_completion_" + suffix)
	triggerName := quoteIdentifier("fail_skill_import_completion_trigger_" + suffix)
	if _, err := testPool.Exec(context.Background(), fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.idempotency_key = %s::uuid THEN
				RAISE EXCEPTION 'forced skill import completion failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON skill_import_request
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, quoteSQLLiteral(key), triggerName, functionName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON skill_import_request`, triggerName))
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})

	w := httptest.NewRecorder()
	req := newRequestAsUser(testUserID, http.MethodPost, "/api/skills/import", map[string]any{"url": importURL})
	req.Header.Set("Idempotency-Key", key)
	testHandler.ImportSkill(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("forced completion failure = %d %s, want 500", w.Code, w.Body.String())
	}
	var skills, requests int
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM skill WHERE workspace_id = $1 AND name = $2),
			(SELECT count(*) FROM skill_import_request WHERE workspace_id = $1 AND idempotency_key = $3)
	`, testWorkspaceID, skillName, key).Scan(&skills, &requests); err != nil {
		t.Fatal(err)
	}
	if skills != 0 || requests != 0 {
		t.Fatalf("failed import left skills=%d requests=%d, want 0/0", skills, requests)
	}
}
