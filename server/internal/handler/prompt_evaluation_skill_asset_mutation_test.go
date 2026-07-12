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

func TestPromptEvaluationSkillAssetMutationsPreserveConcurrentWritesAndReplayExactly(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	repoPath := t.TempDir()
	skillPath := ".codebuddy/skills/verify/SKILL.md"
	runSkillTestGit(t, repoPath, "init")
	runSkillTestGit(t, repoPath, "config", "user.email", "test@multica.local")
	runSkillTestGit(t, repoPath, "config", "user.name", "Multica Test")
	writeSkillTestFile(t, repoPath, skillPath, "# Verify\n\nRun focused checks.\n")
	runSkillTestGit(t, repoPath, "add", ".")
	runSkillTestGit(t, repoPath, "commit", "-m", "add verify skill")
	writeSkillTestFile(t, repoPath, skillPath, "# Verify\n\nRun focused checks and record evidence.\n")
	runSkillTestGit(t, repoPath, "add", ".")
	runSkillTestGit(t, repoPath, "commit", "-m", "require evidence")

	ctx := context.Background()
	assetName := "skill mutation atomicity " + uuid.NewString()
	var assetID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_asset (workspace_id, name, asset_type, payload, created_by)
		VALUES ($1, $2, '测试套件', '{"fixture":"preserve-me"}'::jsonb, $3)
		RETURNING id
	`, testWorkspaceID, assetName, testUserID).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_evaluation_asset WHERE id=$1`, assetID)
	})

	type call struct {
		key     string
		body    map[string]any
		handler func(http.ResponseWriter, *http.Request)
		path    string
	}
	invoke := func(item call) *httptest.ResponseRecorder {
		req := newRequest(http.MethodPost, item.path, item.body)
		req.Header.Set("Idempotency-Key", item.key)
		w := httptest.NewRecorder()
		item.handler(w, withURLParam(req, "id", assetID))
		return w
	}
	inventory := call{
		key: uuid.NewString(), path: "/api/prompt-evaluation-assets/" + assetID + "/skill-inventory",
		body:    map[string]any{"repo_path": repoPath, "skill_root": ".codebuddy/skills", "branch": "HEAD"},
		handler: testHandler.CreatePromptEvaluationSkillInventory,
	}
	snapshot := call{
		key: uuid.NewString(), path: "/api/prompt-evaluation-assets/" + assetID + "/skill-snapshot",
		body:    map[string]any{"repo_path": repoPath, "skill_path": skillPath, "branch": "HEAD"},
		handler: testHandler.CreatePromptEvaluationSkillSnapshot,
	}

	responses := make(chan *httptest.ResponseRecorder, 2)
	var group sync.WaitGroup
	for _, item := range []call{inventory, snapshot} {
		group.Add(1)
		go func() {
			defer group.Done()
			responses <- invoke(item)
		}()
	}
	group.Wait()
	close(responses)
	for response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("concurrent skill mutation = %d %s", response.Code, response.Body.String())
		}
	}

	drafts := call{
		key: uuid.NewString(), path: "/api/prompt-evaluation-assets/" + assetID + "/skill-case-drafts",
		body:    map[string]any{"repo_path": repoPath, "skill_path": skillPath, "limit": 5, "auto_approve": true},
		handler: testHandler.CreatePromptEvaluationSkillCaseDrafts,
	}
	first := invoke(drafts)
	replay := invoke(drafts)
	if first.Code != http.StatusCreated {
		t.Fatalf("first case drafts = %d %s", first.Code, first.Body.String())
	}
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("case draft replay = %d %s, want exact %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	changed := drafts
	changed.body = map[string]any{"repo_path": repoPath, "skill_path": skillPath, "limit": 1, "auto_approve": true}
	conflict := invoke(changed)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed case draft request = %d %s, want 409", conflict.Code, conflict.Body.String())
	}

	var payload []byte
	if err := testPool.QueryRow(ctx, `SELECT payload FROM prompt_evaluation_asset WHERE id=$1`, assetID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(payload, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["fixture"] != "preserve-me" || stored["skill_inventory"] == nil || stored["skill_snapshot"] == nil {
		t.Fatalf("concurrent payload lost a field: %s", payload)
	}
	draftList, ok := stored["skill_case_drafts"].([]any)
	if !ok || len(draftList) != 2 {
		t.Fatalf("case drafts after exact replay = %#v, want one two-commit batch", stored["skill_case_drafts"])
	}

	rollbackKey := uuid.NewString()
	suffix := uuid.NewString()
	functionName := "fail_skill_mutation_complete_" + suffix[:8]
	triggerName := "fail_skill_mutation_complete_" + suffix[9:13]
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected skill mutation completion failure'; END $$;
		CREATE TRIGGER %s BEFORE UPDATE ON resource_create_request
		FOR EACH ROW WHEN (NEW.idempotency_key = '%s'::uuid)
		EXECUTE FUNCTION %s()
	`, functionName, triggerName, rollbackKey, functionName)); err != nil {
		t.Fatal(err)
	}
	dropFailureWitness := func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(
			`DROP TRIGGER IF EXISTS %s ON resource_create_request; DROP FUNCTION IF EXISTS %s()`,
			triggerName, functionName,
		))
	}
	t.Cleanup(dropFailureWitness)
	rollback := drafts
	rollback.key = rollbackKey
	failed := invoke(rollback)
	dropFailureWitness()
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("completion failure = %d %s, want 500", failed.Code, failed.Body.String())
	}
	var payloadAfter []byte
	var leakedRequestCount int
	if err := testPool.QueryRow(ctx, `SELECT payload,
		(SELECT count(*) FROM resource_create_request WHERE idempotency_key=$2)
		FROM prompt_evaluation_asset WHERE id=$1`, assetID, rollbackKey).Scan(&payloadAfter, &leakedRequestCount); err != nil {
		t.Fatal(err)
	}
	if string(payloadAfter) != string(payload) || leakedRequestCount != 0 {
		t.Fatalf("completion failure leaked writes: payload_changed=%t request_count=%d",
			string(payloadAfter) != string(payload), leakedRequestCount)
	}
}
