package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func createRunningCompanionTaskForTest(t *testing.T, agentID, sessionID string) string {
	t.Helper()
	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, chat_session_id, initiator_user_id,
			status, priority, started_at
		) VALUES ($1, $2, $3, $4, 'running', 0, now())
		RETURNING id
	`, agentID, handlerTestRuntimeID(t), sessionID, testUserID).Scan(&taskID); err != nil {
		t.Fatalf("create running companion task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})
	return taskID
}

func callCompanionAgentHandler(t *testing.T, taskID, agentID, method, path string, body any, params map[string]string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest(method, path, body)
	setTaskTokenActor(req, agentID, taskID)
	for key, value := range params {
		req = withURLParam(req, key, value)
	}
	w := httptest.NewRecorder()
	fn(w, req)
	return w
}

func TestCompanionAgentCreatesCandidateDraftAndChoosesSilence(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "LifeAgentAutonomyAgent", nil)
	configureLifeCompanionForTest(t, agentID)
	sessionID := createHandlerTestChatSession(t, agentID)
	var messageID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content)
		VALUES ($1, 'user', '我今天工作很烦，但还没想好要不要换工作。')
		RETURNING id
	`, sessionID).Scan(&messageID); err != nil {
		t.Fatalf("create companion evidence message: %v", err)
	}
	taskID := createRunningCompanionTaskForTest(t, agentID, sessionID)

	w := callCompanionAgentHandler(t, taskID, agentID, http.MethodPost, "/api/life/agent/memory-candidates", map[string]any{
		"kind":        "weak_signal",
		"content":     "当前工作压力可能正在上升，但尚不能判断是否真的想换工作",
		"confidence":  0.58,
		"urgency":     0.3,
		"uncertainty": "只有一次表达，也可能只是今天的具体事件",
		"evidence": []map[string]any{{
			"source_type": "chat_message",
			"source_id":   messageID,
		}},
	}, nil, testHandler.CreateCompanionMemoryCandidate)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateCompanionMemoryCandidate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var candidate lifeMemoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &candidate); err != nil {
		t.Fatalf("decode companion candidate: %v", err)
	}
	if candidate.CreatedByType != "agent" || candidate.CreatedByID != agentID || candidate.Status != "candidate" {
		t.Fatalf("companion candidate identity/status mismatch: %#v", candidate)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM life_memory WHERE id = $1`, candidate.ID) })

	start := time.Now().Add(24 * time.Hour).UTC()
	w = callCompanionAgentHandler(t, taskID, agentID, http.MethodPost, "/api/life/agent/proposals", map[string]any{
		"proposal_type": "experiment_start",
		"status":        "internal_draft",
		"title":         "三天压力触发点记录",
		"summary":       "先记录触发点，不急着得出换工作的结论。",
		"payload": map[string]any{
			"problem":     "工作压力的来源还不清楚",
			"hypothesis":  "记录具体触发点能区分短期情绪和持续趋势",
			"method":      map[string]any{"frequency": "遇到明显压力时"},
			"plan":        map[string]any{"fields": []string{"事件", "想法", "情绪", "身体反应"}},
			"starts_at":   start.Format(time.RFC3339),
			"ends_at":     start.Add(72 * time.Hour).Format(time.RFC3339),
			"memory_ids":  []string{candidate.ID},
			"issue_title": "记录三天压力触发点",
		},
	}, nil, testHandler.CreateCompanionActionProposal)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateCompanionActionProposal: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var proposal lifeProposalResponse
	if err := json.Unmarshal(w.Body.Bytes(), &proposal); err != nil {
		t.Fatalf("decode companion proposal: %v", err)
	}
	if proposal.Status != "internal_draft" {
		t.Fatalf("proposal should remain inside companion workspace: %#v", proposal)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM life_action_proposal WHERE id = $1`, proposal.ID) })

	w = callLifeHandler(t, http.MethodGet, "/api/life/proposals", nil, nil, testHandler.ListLifeActionProposals)
	if w.Code != http.StatusOK || w.Body.String() != "{\"proposals\":[]}\n" {
		t.Fatalf("internal draft leaked into member proposal list: %d %s", w.Code, w.Body.String())
	}
	w = callCompanionAgentHandler(t, taskID, agentID, http.MethodPost, "/api/life/agent/proposals/"+proposal.ID+"/present", nil,
		map[string]string{"proposalId": proposal.ID}, testHandler.PresentCompanionActionProposal)
	if w.Code != http.StatusOK {
		t.Fatalf("PresentCompanionActionProposal: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	w = callLifeHandler(t, http.MethodGet, "/api/life/proposals", nil, nil, testHandler.ListLifeActionProposals)
	if w.Code != http.StatusOK || !json.Valid(w.Body.Bytes()) || !containsJSONText(w.Body.Bytes(), proposal.Title) {
		t.Fatalf("presented proposal not visible for confirmation: %d %s", w.Code, w.Body.String())
	}

	w = callCompanionAgentHandler(t, taskID, agentID, http.MethodPost, "/api/life/agent/proactive-checks", map[string]any{
		"status":         "silent",
		"trigger_source": "manual",
		"reason":         "刚完成对话，用户已经得到支撑；现在追加提醒只会增加负担",
		"context_snapshot": map[string]any{
			"latest_message_id": messageID,
			"decision":          "do_not_interrupt",
		},
	}, nil, testHandler.CreateCompanionProactiveCheck)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateCompanionProactiveCheck: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM life_proactive_check WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, testUserID)
	})
	w = callLifeHandler(t, http.MethodGet, "/api/life/proactive-checks", nil, nil, testHandler.ListLifeProactiveChecks)
	if w.Code != http.StatusOK || !containsJSONText(w.Body.Bytes(), "silent") || !containsJSONText(w.Body.Bytes(), "do_not_interrupt") {
		t.Fatalf("silent proactive decision not observable: %d %s", w.Code, w.Body.String())
	}
}

func TestLifeAgentResolvesOnlyGovernedEvidence(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "LifeEvidenceResolverAgent", nil)
	configureLifeCompanionForTest(t, agentID)
	sessionID := createHandlerTestChatSession(t, agentID)
	var messageID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content)
		VALUES ($1, 'user', '这是一条只属于当前人生资料库的证据。')
		RETURNING id
	`, sessionID).Scan(&messageID); err != nil {
		t.Fatalf("create life evidence message: %v", err)
	}
	var materialID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM life_material
		WHERE workspace_id=$1 AND user_id=$2 AND source_type='chat_message' AND source_key=$3
	`, testWorkspaceID, testUserID, messageID).Scan(&materialID); err != nil {
		t.Fatalf("load captured life material: %v", err)
	}
	taskID := createRunningCompanionTaskForTest(t, agentID, sessionID)
	w := callCompanionAgentHandler(t, taskID, agentID, http.MethodPost, "/api/life/agent/evidence/resolve", map[string]any{
		"references": []map[string]any{
			{"source_type": "material", "source_id": materialID},
			{"source_type": "material", "source_id": uuid.NewString()},
		},
	}, nil, testHandler.ResolveLifeEvidence)
	if w.Code != http.StatusOK {
		t.Fatalf("ResolveLifeEvidence: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Evidence []struct {
			Available bool           `json:"available"`
			Material  map[string]any `json:"material"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode resolved life evidence: %v", err)
	}
	if len(response.Evidence) != 2 || !response.Evidence[0].Available || response.Evidence[1].Available {
		t.Fatalf("unexpected evidence availability: %#v", response.Evidence)
	}
	if response.Evidence[0].Material["content"] != "这是一条只属于当前人生资料库的证据。" {
		t.Fatalf("resolved material mismatch: %#v", response.Evidence[0].Material)
	}
}

func containsJSONText(raw []byte, value string) bool {
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return false
	}
	encoded, _ := json.Marshal(decoded)
	needle, _ := json.Marshal(value)
	return len(needle) > 2 && strings.Contains(string(encoded), string(needle[1:len(needle)-1]))
}

func TestLifeChronicleSeparatesTimeLayersAndEvidence(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "LifeChronicleAgent", nil)
	messageID := createLifeChatEvidence(t, agentID, "这周我三次说不想干了，但仍在按跳槽计划准备。")
	memory := createLifeMemoryForTest(t, "chat_message", messageID, "weak_signal", "本周工作压力信号反复出现")

	periodEnd := time.Now().UTC()
	periodStart := periodEnd.Add(-7 * 24 * time.Hour)
	w := callLifeHandler(t, http.MethodPost, "/api/life/chronicle", map[string]any{
		"period_start":       periodStart.Format(time.RFC3339),
		"period_end":         periodEnd.Format(time.RFC3339),
		"facts":              "本周三次表达不想继续当前工作；仍完成了两项跳槽准备任务。",
		"feelings":           "疲惫、烦躁，也有一点推进后的踏实。",
		"understanding_then": "当时更像是高压下想立刻逃离。",
		"evidence": []map[string]any{{
			"source_type": "memory",
			"source_id":   memory.ID,
		}, {
			"source_type": "chat_message",
			"source_id":   messageID,
		}},
	}, nil, testHandler.CreateLifeChronicleEntry)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateLifeChronicleEntry: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var entry lifeChronicleResponse
	if err := json.Unmarshal(w.Body.Bytes(), &entry); err != nil {
		t.Fatalf("decode chronicle entry: %v", err)
	}
	if entry.Facts == "" || entry.Feelings == "" || entry.UnderstandingThen == "" || entry.UnderstandingLater != "" || len(entry.Evidence) != 2 {
		t.Fatalf("chronicle layers/evidence were not separated: %#v", entry)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM life_chronicle_entry WHERE id = $1`, entry.ID) })

	w = callLifeHandler(t, http.MethodPatch, "/api/life/chronicle/"+entry.ID+"/later-understanding", map[string]any{
		"understanding_later": "后来结合一周记录看，反复出现的是会议失控感，不等于已经决定离职。",
	}, map[string]string{"entryId": entry.ID}, testHandler.UpdateLifeChronicleLaterUnderstanding)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateLifeChronicleLaterUnderstanding: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated lifeChronicleResponse
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated chronicle: %v", err)
	}
	if updated.Facts != entry.Facts || updated.UnderstandingThen != entry.UnderstandingThen || updated.UnderstandingLater == "" {
		t.Fatalf("later understanding overwrote historical layers: before=%#v after=%#v", entry, updated)
	}
}
