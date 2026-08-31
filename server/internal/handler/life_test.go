package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func callLifeHandler(t *testing.T, method, path string, body any, params map[string]string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest(method, path, body)
	for key, value := range params {
		req = withURLParam(req, key, value)
	}
	w := httptest.NewRecorder()
	fn(w, req)
	return w
}

func TestListLifeChronicleEntriesClientCanceledReturns499(t *testing.T) {
	req := newRequest(http.MethodGet, "/api/life/chronicle", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	newCanceledLifeReadHandler().ListLifeChronicleEntries(w, req)
	if w.Code != 499 {
		t.Fatalf("expected 499 for canceled chronicle read, got %d: %s", w.Code, w.Body.String())
	}
}

type canceledLifeReadDB struct{}

func (canceledLifeReadDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, context.Canceled
}

func (canceledLifeReadDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, context.Canceled
}

func (canceledLifeReadDB) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	return errorRow{err: context.Canceled}
}

func newCanceledLifeReadHandler() *Handler {
	return &Handler{Queries: db.New(canceledLifeReadDB{})}
}

func createLifeMemoryForTest(t *testing.T, evidenceType, evidenceID, kind, content string) lifeMemoryResponse {
	t.Helper()
	w := callLifeHandler(t, http.MethodPost, "/api/life/memories", map[string]any{
		"kind":        kind,
		"content":     content,
		"confidence":  0.62,
		"urgency":     0.35,
		"uncertainty": "目前只有一次表达，需要继续观察",
		"evidence": []map[string]any{{
			"source_type": evidenceType,
			"source_id":   evidenceID,
		}},
	}, nil, testHandler.CreateLifeMemory)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateLifeMemory: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var memory lifeMemoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &memory); err != nil {
		t.Fatalf("decode memory: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_memory WHERE id = $1`, memory.ID)
	})
	return memory
}

func createLifeChatEvidence(t *testing.T, agentID, content string) string {
	t.Helper()
	sessionID := createHandlerTestChatSession(t, agentID)
	var messageID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO chat_message (chat_session_id, role, content)
		VALUES ($1, 'user', $2)
		RETURNING id
	`, sessionID, content).Scan(&messageID); err != nil {
		t.Fatalf("create chat evidence: %v", err)
	}
	return messageID
}

func TestLifeCompanionAndMemoryGovernance(t *testing.T) {
	requireHandlerDatabase(t)
	agentID := createHandlerTestAgent(t, "LifeCompanionGovernanceAgent", nil)
	messageID := createLifeChatEvidence(t, agentID, "我不想干了，但这不等于我已经决定离职。")

	w := callLifeHandler(t, http.MethodPut, "/api/life/companion", map[string]any{
		"agent_id": agentID,
	}, nil, testHandler.UpsertCompanionProfile)
	if w.Code != http.StatusOK {
		t.Fatalf("UpsertCompanionProfile: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM companion_profile WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, testUserID)
	})

	w = callLifeHandler(t, http.MethodGet, "/api/life/companion", nil, nil, testHandler.GetCompanionProfile)
	if w.Code != http.StatusOK {
		t.Fatalf("GetCompanionProfile: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var profileEnvelope struct {
		Profile companionProfileResponse `json:"profile"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &profileEnvelope); err != nil {
		t.Fatalf("decode companion profile: %v", err)
	}
	if profileEnvelope.Profile.AgentID != agentID {
		t.Fatalf("companion agent: want %s, got %s", agentID, profileEnvelope.Profile.AgentID)
	}

	memory := createLifeMemoryForTest(t, "chat_message", messageID, "weak_signal", "最近出现离职冲动，但尚未形成离职决定")
	if memory.Status != "candidate" || memory.Confidence != 0.62 || memory.Uncertainty == "" {
		t.Fatalf("candidate metadata missing: %#v", memory)
	}
	if len(memory.Evidence) != 1 || memory.Evidence[0].SourceID != messageID || memory.Evidence[0].Excerpt == "" {
		t.Fatalf("candidate evidence missing: %#v", memory.Evidence)
	}

	w = callLifeHandler(t, http.MethodGet, "/api/life/memories?status=candidate", nil, nil, testHandler.ListLifeMemories)
	if w.Code != http.StatusOK {
		t.Fatalf("ListLifeMemories: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listEnvelope struct {
		Memories []lifeMemoryResponse `json:"memories"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listEnvelope); err != nil {
		t.Fatalf("decode memory list: %v", err)
	}
	if len(listEnvelope.Memories) == 0 {
		t.Fatal("candidate memory was not listed")
	}

	params := map[string]string{"memoryId": memory.ID}
	w = callLifeHandler(t, http.MethodPost, "/api/life/memories/"+memory.ID+"/confirm", nil, params, testHandler.ConfirmLifeMemory)
	if w.Code != http.StatusOK {
		t.Fatalf("ConfirmLifeMemory: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var confirmed lifeMemoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &confirmed); err != nil {
		t.Fatalf("decode confirmed memory: %v", err)
	}
	if confirmed.Status != "confirmed" || confirmed.ConfirmedAt == nil {
		t.Fatalf("memory was not confirmed: %#v", confirmed)
	}

	w = callLifeHandler(t, http.MethodPatch, "/api/life/memories/"+memory.ID, map[string]any{
		"kind":        "understanding",
		"content":     "高压时会出现离职冲动；是否改变工作仍需综合判断",
		"confidence":  0.78,
		"urgency":     0.4,
		"uncertainty": "需要结合持续时间、工作事件和既定离职计划",
		"valid_from":  time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	}, params, testHandler.UpdateLifeMemory)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateLifeMemory: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var revised lifeMemoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &revised); err != nil {
		t.Fatalf("decode revised memory: %v", err)
	}
	if revised.Status != "confirmed" || revised.Content == confirmed.Content || revised.Kind != "understanding" {
		t.Fatalf("confirmed memory was not revised in place: %#v", revised)
	}

	w = callLifeHandler(t, http.MethodPost, "/api/life/memories/"+memory.ID+"/downgrade", map[string]any{
		"kind": "current_expression",
	}, params, testHandler.DowngradeLifeMemory)
	if w.Code != http.StatusOK {
		t.Fatalf("DowngradeLifeMemory: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var downgraded lifeMemoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &downgraded); err != nil {
		t.Fatalf("decode downgraded memory: %v", err)
	}
	if downgraded.Status != "candidate" || downgraded.Kind != "current_expression" || downgraded.ConfirmedAt != nil {
		t.Fatalf("memory was not downgraded to a candidate: %#v", downgraded)
	}

	w = callLifeHandler(t, http.MethodPost, "/api/life/memories/"+memory.ID+"/archive", nil, params, testHandler.ArchiveLifeMemory)
	if w.Code != http.StatusOK {
		t.Fatalf("ArchiveLifeMemory: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var archived lifeMemoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &archived); err != nil {
		t.Fatalf("decode archived memory: %v", err)
	}
	if archived.Status != "archived" {
		t.Fatalf("memory was not archived: %#v", archived)
	}
}

func TestDeleteLifeMemoryPropagatesToDerivedRecords(t *testing.T) {
	requireHandlerDatabase(t)
	agentID := createHandlerTestAgent(t, "LifeMemoryDeletionAgent", nil)
	messageID := createLifeChatEvidence(t, agentID, "我想连续一周写心情日记。")
	root := createLifeMemoryForTest(t, "chat_message", messageID, "plan", "连续一周写心情日记")
	derived := createLifeMemoryForTest(t, "memory", root.ID, "understanding", "心情记录可能帮助识别压力模式")

	ctx := context.Background()
	var proposalID, experimentID, roundID, chronicleID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO life_action_proposal (
			workspace_id, user_id, companion_agent_id, proposal_type, title
		) VALUES ($1, $2, $3, 'experiment_start', '心情日记实验')
		RETURNING id
	`, testWorkspaceID, testUserID, agentID).Scan(&proposalID); err != nil {
		t.Fatalf("create proposal dependency: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO life_experiment (
			workspace_id, user_id, title, problem, hypothesis, created_by_type, created_by_id
		) VALUES ($1, $2, '心情日记', '识别压力模式', '记录会帮助识别模式', 'member', $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&experimentID); err != nil {
		t.Fatalf("create experiment dependency: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO life_experiment_round (experiment_id, proposal_id, status)
		VALUES ($1, $2, 'draft')
		RETURNING id
	`, experimentID, proposalID).Scan(&roundID); err != nil {
		t.Fatalf("create experiment round dependency: %v", err)
	}
	mustExec(t, ctx, `INSERT INTO life_experiment_memory (round_id, memory_id, role) VALUES ($1, $2, 'input')`, roundID, derived.ID)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO life_chronicle_entry (
			workspace_id, user_id, period_start, period_end, facts, understanding_then
		) VALUES ($1, $2, now() - interval '1 day', now(), '开始准备心情日记实验', '记录可能有帮助')
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&chronicleID); err != nil {
		t.Fatalf("create chronicle dependency: %v", err)
	}
	mustExec(t, ctx, `INSERT INTO life_chronicle_evidence (entry_id, source_type, source_id) VALUES ($1, 'experiment_round', $2)`, chronicleID, roundID)

	w := callLifeHandler(t, http.MethodDelete, "/api/life/memories/"+root.ID, nil, map[string]string{"memoryId": root.ID}, testHandler.DeleteLifeMemory)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteLifeMemory: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	checks := []struct {
		name  string
		query string
		id    string
	}{
		{"root memory", `SELECT count(*) FROM life_memory WHERE id = $1`, root.ID},
		{"derived memory", `SELECT count(*) FROM life_memory WHERE id = $1`, derived.ID},
		{"proposal", `SELECT count(*) FROM life_action_proposal WHERE id = $1`, proposalID},
		{"experiment round", `SELECT count(*) FROM life_experiment_round WHERE id = $1`, roundID},
		{"chronicle", `SELECT count(*) FROM life_chronicle_entry WHERE id = $1`, chronicleID},
	}
	for _, check := range checks {
		var count int
		if err := testPool.QueryRow(ctx, check.query, check.id).Scan(&count); err != nil {
			t.Fatalf("check %s deletion: %v", check.name, err)
		}
		if count != 0 {
			t.Fatalf("%s survived permanent deletion", check.name)
		}
	}

	var experimentCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM life_experiment WHERE id = $1`, experimentID).Scan(&experimentCount); err != nil {
		t.Fatalf("check experiment retention: %v", err)
	}
	if experimentCount != 1 {
		t.Fatal("experiment definition should survive deletion of one dependent round")
	}
	mustExec(t, ctx, `DELETE FROM life_experiment WHERE id = $1`, experimentID)
}
