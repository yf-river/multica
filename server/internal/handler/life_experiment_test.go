package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

func configureLifeCompanionForTest(t *testing.T, agentID string) {
	t.Helper()
	w := callLifeHandler(t, http.MethodPut, "/api/life/companion", map[string]any{
		"agent_id": agentID,
	}, nil, testHandler.UpsertCompanionProfile)
	if w.Code != http.StatusOK {
		t.Fatalf("configure companion: %d %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM companion_profile WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, testUserID)
	})
}

func createExperimentProposalForTest(t *testing.T, proposalType, title string, payload map[string]any) lifeProposalResponse {
	t.Helper()
	w := callLifeHandler(t, http.MethodPost, "/api/life/proposals", map[string]any{
		"proposal_type": proposalType,
		"title":         title,
		"summary":       "先做一个有边界、可停止、可复盘的小实验",
		"payload":       payload,
	}, nil, testHandler.CreateLifeActionProposal)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateLifeActionProposal: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var proposal lifeProposalResponse
	if err := json.Unmarshal(w.Body.Bytes(), &proposal); err != nil {
		t.Fatalf("decode proposal: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_action_proposal WHERE id = $1`, proposal.ID)
	})
	return proposal
}

func TestLifeExperimentConfirmationIsAtomicAndRoundsAreIndependent(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "LifeExperimentAgent", nil)
	configureLifeCompanionForTest(t, agentID)
	messageID := createLifeChatEvidence(t, agentID, "我想试试连续记录三天心情。")
	memory := createLifeMemoryForTest(t, "chat_message", messageID, "plan", "连续记录三天心情")

	start := time.Now().Add(time.Minute).UTC()
	end := start.Add(72 * time.Hour)
	basePayload := map[string]any{
		"problem":           "最近很难看清压力从哪里来",
		"hypothesis":        "连续记录心情能帮助识别压力模式",
		"method":            map[string]any{"frequency": "每天一次"},
		"plan":              map[string]any{"prompt": "今天发生了什么，我有什么感受"},
		"starts_at":         start.Format(time.RFC3339),
		"ends_at":           end.Format(time.RFC3339),
		"issue_title":       "完成三天心情日记实验",
		"issue_description": "每天记录一次，实验结束后一起复盘。",
	}

	var counterBefore int32
	if err := testPool.QueryRow(ctx, `SELECT issue_counter FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&counterBefore); err != nil {
		t.Fatalf("load issue counter: %v", err)
	}
	failingPayload := make(map[string]any, len(basePayload)+1)
	for key, value := range basePayload {
		failingPayload[key] = value
	}
	failingPayload["memory_ids"] = []string{memory.ID, uuid.NewString()}
	failingProposal := createExperimentProposalForTest(t, "experiment_start", "三天心情日记", failingPayload)
	w := callLifeHandler(t, http.MethodPost, "/api/life/proposals/"+failingProposal.ID+"/confirm", nil,
		map[string]string{"proposalId": failingProposal.ID}, testHandler.ConfirmLifeActionProposal)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("confirmation with missing memory: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var experimentCount, issueCount, roundCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM life_experiment WHERE workspace_id = $1 AND title = '三天心情日记'`, testWorkspaceID).Scan(&experimentCount); err != nil {
		t.Fatalf("count rolled back experiments: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE workspace_id = $1 AND title = '完成三天心情日记实验'`, testWorkspaceID).Scan(&issueCount); err != nil {
		t.Fatalf("count rolled back issues: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM life_experiment_round WHERE proposal_id = $1`, failingProposal.ID).Scan(&roundCount); err != nil {
		t.Fatalf("count rolled back rounds: %v", err)
	}
	var counterAfter int32
	if err := testPool.QueryRow(ctx, `SELECT issue_counter FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&counterAfter); err != nil {
		t.Fatalf("reload issue counter: %v", err)
	}
	if experimentCount != 0 || issueCount != 0 || roundCount != 0 || counterAfter != counterBefore {
		t.Fatalf("partial experiment start survived rollback: experiments=%d issues=%d rounds=%d counter=%d->%d", experimentCount, issueCount, roundCount, counterBefore, counterAfter)
	}

	validPayload := make(map[string]any, len(basePayload)+1)
	for key, value := range basePayload {
		validPayload[key] = value
	}
	validPayload["memory_ids"] = []string{memory.ID}
	proposal := createExperimentProposalForTest(t, "experiment_start", "三天心情日记", validPayload)
	w = callLifeHandler(t, http.MethodPost, "/api/life/proposals/"+proposal.ID+"/confirm", nil,
		map[string]string{"proposalId": proposal.ID}, testHandler.ConfirmLifeActionProposal)
	if w.Code != http.StatusCreated {
		t.Fatalf("ConfirmLifeActionProposal: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var started struct {
		ExperimentID string                      `json:"experiment_id"`
		IssueID      string                      `json:"issue_id"`
		Round        lifeExperimentRoundResponse `json:"round"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode started experiment: %v", err)
	}
	if started.ExperimentID == "" || started.IssueID == "" || started.Round.Status != "running" {
		t.Fatalf("experiment did not fully start: %#v", started)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM life_experiment WHERE id = $1`, started.ExperimentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, started.IssueID)
	})
	var issueScope, assigneeType, assigneeID string
	if err := testPool.QueryRow(ctx, `SELECT scope, assignee_type, assignee_id::text FROM issue WHERE id = $1`, started.IssueID).Scan(&issueScope, &assigneeType, &assigneeID); err != nil {
		t.Fatalf("load experiment task: %v", err)
	}
	if issueScope != "personal" || assigneeType != "member" || assigneeID != testUserID {
		t.Fatalf("experiment task ownership mismatch: scope=%s assignee=%s/%s", issueScope, assigneeType, assigneeID)
	}

	mustExec(t, ctx, `
		UPDATE life_experiment_round
		SET starts_at = now() - interval '2 hours', ends_at = now() - interval '1 hour'
		WHERE id = $1
	`, started.Round.ID)
	w = callLifeHandler(t, http.MethodGet, "/api/life/experiments", nil, nil, testHandler.ListLifeExperiments)
	if w.Code != http.StatusOK {
		t.Fatalf("ListLifeExperiments: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var expiredStatus, expiredReason string
	var stoppedAt *time.Time
	if err := testPool.QueryRow(ctx, `SELECT status, stop_reason, stopped_at FROM life_experiment_round WHERE id = $1`, started.Round.ID).Scan(&expiredStatus, &expiredReason, &stoppedAt); err != nil {
		t.Fatalf("load expired round: %v", err)
	}
	if expiredStatus != "awaiting_review" || expiredReason != "expired" || stoppedAt == nil {
		t.Fatalf("expired round did not stop by default: status=%s reason=%s stopped=%v", expiredStatus, expiredReason, stoppedAt)
	}

	w = callLifeHandler(t, http.MethodPost, "/api/life/experiment-rounds/"+started.Round.ID+"/review", map[string]any{
		"outcome":              "完成了两次记录，识别出会议后的压力峰值",
		"feelings":             "第二天更容易觉察情绪",
		"burden":               "晚间记录有一点负担",
		"companion_correction": "下一轮改成午饭后提醒，并允许只写一句话",
	}, map[string]string{"roundId": started.Round.ID}, testHandler.ReviewLifeExperimentRound)
	if w.Code != http.StatusOK {
		t.Fatalf("ReviewLifeExperimentRound: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var reviewed lifeExperimentRoundResponse
	if err := json.Unmarshal(w.Body.Bytes(), &reviewed); err != nil {
		t.Fatalf("decode reviewed round: %v", err)
	}
	if reviewed.Status != "reviewed" || !json.Valid(reviewed.Review) {
		t.Fatalf("round review missing: %#v", reviewed)
	}

	rerunStart := time.Now().Add(24 * time.Hour).UTC()
	rerunPayload := map[string]any{
		"experiment_id":     started.ExperimentID,
		"previous_round_id": started.Round.ID,
		"problem":           basePayload["problem"],
		"hypothesis":        basePayload["hypothesis"],
		"method":            basePayload["method"],
		"plan":              map[string]any{"prompt": "午饭后用一句话记录心情"},
		"starts_at":         rerunStart.Format(time.RFC3339),
		"ends_at":           rerunStart.Add(72 * time.Hour).Format(time.RFC3339),
		"memory_ids":        []string{memory.ID},
		"issue_title":       "再次执行心情日记实验",
	}
	rerunProposal := createExperimentProposalForTest(t, "experiment_extend", "三天心情日记（第二轮）", rerunPayload)
	w = callLifeHandler(t, http.MethodPost, "/api/life/proposals/"+rerunProposal.ID+"/confirm", nil,
		map[string]string{"proposalId": rerunProposal.ID}, testHandler.ConfirmLifeActionProposal)
	if w.Code != http.StatusCreated {
		t.Fatalf("confirm rerun: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var rerun struct {
		ExperimentID string                      `json:"experiment_id"`
		IssueID      string                      `json:"issue_id"`
		Round        lifeExperimentRoundResponse `json:"round"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rerun); err != nil {
		t.Fatalf("decode rerun: %v", err)
	}
	if rerun.ExperimentID != started.ExperimentID || rerun.Round.ID == started.Round.ID || rerun.Round.PreviousRoundID == nil || *rerun.Round.PreviousRoundID != started.Round.ID {
		t.Fatalf("rerun was not an independent linked round: first=%#v rerun=%#v", started, rerun)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, rerun.IssueID) })
}
