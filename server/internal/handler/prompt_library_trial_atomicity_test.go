package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreatePromptLibraryTrialRollsBackEveryWrite(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	createW := httptest.NewRecorder()
	testHandler.CreatePromptLibraryItem(createW, newRequest(http.MethodPost, "/api/prompt-library", map[string]any{
		"name":        "atomic trial " + suffix,
		"prompt_type": "通用",
		"content":     "请处理当前输入。",
	}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("create prompt: expected 201, got %d: %s", createW.Code, createW.Body.String())
	}
	var item PromptLibraryItemResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode prompt: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM prompt_library_item WHERE id = $1`, item.ID)
	})

	versionsW := httptest.NewRecorder()
	testHandler.ListPromptLibraryVersions(versionsW, withURLParam(
		newRequest(http.MethodGet, "/api/prompt-library/"+item.ID+"/versions", nil),
		"id",
		item.ID,
	))
	if versionsW.Code != http.StatusOK {
		t.Fatalf("list versions: expected 200, got %d: %s", versionsW.Code, versionsW.Body.String())
	}
	var versions struct {
		Items []PromptLibraryVersionResponse `json:"items"`
	}
	if err := json.Unmarshal(versionsW.Body.Bytes(), &versions); err != nil || len(versions.Items) != 1 {
		t.Fatalf("decode versions: err=%v response=%+v", err, versions)
	}

	agentID := createHandlerTestAgent(t, "atomic-prompt-trial-"+suffix, []byte(`[]`))
	titlePattern := "提示词试跑 · " + item.Name + "%"
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM agent_task_queue
			WHERE chat_session_id IN (SELECT id FROM chat_session WHERE agent_id = $1 AND title LIKE $2)
		`, agentID, titlePattern)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE agent_id = $1 AND title LIKE $2`, agentID, titlePattern)
	})

	functionName := "prompt_trial_fail_fn_" + suffix
	triggerName := "prompt_trial_fail_" + suffix
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON prompt_library_trial`, triggerName))
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
	if _, err := testPool.Exec(context.Background(), fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'forced prompt library trial failure';
		END;
		$$;
		CREATE TRIGGER %s
		BEFORE INSERT ON prompt_library_trial
		FOR EACH ROW WHEN (NEW.prompt_id = '%s')
		EXECUTE FUNCTION %s();
	`, functionName, triggerName, item.ID, functionName)); err != nil {
		t.Fatalf("install prompt trial failure: %v", err)
	}

	versionID := versions.Items[0].ID
	req := newRequest(http.MethodPost, "/api/prompt-library/"+item.ID+"/versions/"+versionID+"/trials", map[string]any{
		"agent_id": agentID,
		"input":    "atomic input",
	})
	req = withURLParams(req, "id", item.ID, "versionId", versionID)
	w := httptest.NewRecorder()
	testHandler.CreatePromptLibraryTrial(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("forced trial failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}

	var sessions, messages, tasks, trials int
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM chat_session WHERE agent_id = $1 AND title LIKE $2),
			(SELECT count(*) FROM chat_message WHERE chat_session_id IN (SELECT id FROM chat_session WHERE agent_id = $1 AND title LIKE $2)),
			(SELECT count(*) FROM agent_task_queue WHERE chat_session_id IN (SELECT id FROM chat_session WHERE agent_id = $1 AND title LIKE $2)),
			(SELECT count(*) FROM prompt_library_trial WHERE prompt_id = $3)
	`, agentID, titlePattern, item.ID).Scan(&sessions, &messages, &tasks, &trials); err != nil {
		t.Fatalf("count prompt trial writes: %v", err)
	}
	if sessions != 0 || messages != 0 || tasks != 0 || trials != 0 {
		t.Fatalf("failed trial left writes: sessions=%d messages=%d tasks=%d trials=%d", sessions, messages, tasks, trials)
	}
	if strings.Contains(w.Body.String(), "forced prompt library trial failure") {
		t.Fatal("database failure details leaked in response")
	}

	if _, err := testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER %s ON prompt_library_trial`, triggerName)); err != nil {
		t.Fatalf("remove prompt trial failure trigger: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION %s()`, functionName)); err != nil {
		t.Fatalf("remove prompt trial failure function: %v", err)
	}

	successReq := newRequest(http.MethodPost, "/api/prompt-library/"+item.ID+"/versions/"+versionID+"/trials", map[string]any{
		"agent_id": agentID,
		"input":    "committed input",
	})
	successReq = withURLParams(successReq, "id", item.ID, "versionId", versionID)
	successW := httptest.NewRecorder()
	testHandler.CreatePromptLibraryTrial(successW, successReq)
	if successW.Code != http.StatusAccepted {
		t.Fatalf("successful trial: expected 202, got %d: %s", successW.Code, successW.Body.String())
	}
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM chat_session WHERE agent_id = $1 AND title LIKE $2),
			(SELECT count(*) FROM chat_message WHERE chat_session_id IN (SELECT id FROM chat_session WHERE agent_id = $1 AND title LIKE $2)),
			(SELECT count(*) FROM agent_task_queue WHERE chat_session_id IN (SELECT id FROM chat_session WHERE agent_id = $1 AND title LIKE $2)),
			(SELECT count(*) FROM prompt_library_trial WHERE prompt_id = $3)
	`, agentID, titlePattern, item.ID).Scan(&sessions, &messages, &tasks, &trials); err != nil {
		t.Fatalf("count committed prompt trial writes: %v", err)
	}
	if sessions != 1 || messages != 1 || tasks != 1 || trials != 1 {
		t.Fatalf("committed trial writes: sessions=%d messages=%d tasks=%d trials=%d", sessions, messages, tasks, trials)
	}
}
