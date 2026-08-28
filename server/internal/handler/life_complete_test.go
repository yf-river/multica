package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCoalesceLifeMemoryEvidenceMergesOneSource(t *testing.T) {
	items := coalesceLifeMemoryEvidence([]lifeJobEvidenceOutput{
		{SourceType: "chat_message", SourceID: "source", Excerpt: "first", Stance: "supports"},
		{SourceType: "chat_message", SourceID: "source", Excerpt: "second", Stance: "supports"},
	})
	if len(items) != 1 || !strings.Contains(items[0].Excerpt, "first") || !strings.Contains(items[0].Excerpt, "second") || items[0].Stance != "supports" {
		t.Fatalf("coalesced evidence = %#v", items)
	}
}

func TestLifeIdentityObserverAndPolicyAreUserGoverned(t *testing.T) {
	requireHandlerDatabase(t)
	companionID := createHandlerTestAgent(t, "CompleteLifeCompanion", nil)
	observerAgentID := createHandlerTestAgent(t, "CompleteLifeObserver", nil)
	configureLifeCompanionForTest(t, companionID)

	w := callLifeHandler(t, http.MethodGet, "/api/life/identity/versions", nil, nil, testHandler.ListLifeIdentityVersions)
	if w.Code != http.StatusOK || !containsJSONText(w.Body.Bytes(), "shared_changes_require_confirmation") {
		t.Fatalf("default identity missing: %d %s", w.Code, w.Body.String())
	}
	w = callLifeHandler(t, http.MethodPost, "/api/life/identity/versions", map[string]any{
		"stable_core": map[string]any{"traits": []string{"直接", "热烈"}}, "relationship_contract": map[string]any{"support_without_control": true},
		"growth_profile": map[string]any{"may_grow": []string{"兴趣"}}, "expression_profile": map[string]any{"strong_language_allowed": true},
		"interests": []string{"心理学"}, "change_reason": "测试人格版本确认链",
	}, nil, testHandler.CreateLifeIdentityVersion)
	if w.Code != http.StatusCreated {
		t.Fatalf("create identity draft: %d %s", w.Code, w.Body.String())
	}
	var identity map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &identity)
	identityID, _ := identity["id"].(string)
	w = callLifeHandler(t, http.MethodPost, "/api/life/identity/versions/"+identityID+"/activate", nil, map[string]string{"versionId": identityID}, testHandler.ActivateLifeIdentityVersion)
	if w.Code != http.StatusOK || !containsJSONText(w.Body.Bytes(), "active") {
		t.Fatalf("activate identity: %d %s", w.Code, w.Body.String())
	}
	w = callLifeHandler(t, http.MethodPost, "/api/life/observers", map[string]any{
		"agent_id": companionID, "name": "伪独立视角", "basis_type": "virtual",
		"personality": map[string]any{}, "perspective": map[string]any{}, "expression_profile": map[string]any{},
	}, nil, testHandler.CreateLifeObserver)
	if w.Code != http.StatusConflict {
		t.Fatalf("companion must not double as an independent observer: %d %s", w.Code, w.Body.String())
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET model='different-life-model' WHERE id=$1`, observerAgentID); err != nil {
		t.Fatalf("set mismatched observer model: %v", err)
	}
	w = callLifeHandler(t, http.MethodPost, "/api/life/observers", map[string]any{
		"agent_id": observerAgentID, "name": "模型漂移视角", "basis_type": "virtual",
		"personality": map[string]any{}, "perspective": map[string]any{}, "expression_profile": map[string]any{},
	}, nil, testHandler.CreateLifeObserver)
	if w.Code != http.StatusConflict {
		t.Fatalf("observer model must match companion: %d %s", w.Code, w.Body.String())
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET model=NULL WHERE id=$1`, observerAgentID); err != nil {
		t.Fatalf("restore observer model: %v", err)
	}

	w = callLifeHandler(t, http.MethodPost, "/api/life/observers", map[string]any{
		"agent_id": observerAgentID, "name": "未来的我", "basis_type": "virtual",
		"personality": map[string]any{"traits": []string{"冷静"}}, "perspective": map[string]any{"focus": []string{"长期代价"}}, "expression_profile": map[string]any{"tone": "直接"},
	}, nil, testHandler.CreateLifeObserver)
	if w.Code != http.StatusCreated {
		t.Fatalf("create observer: %d %s", w.Code, w.Body.String())
	}
	var observer map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &observer)
	observerID, _ := observer["id"].(string)
	w = callLifeHandler(t, http.MethodPost, "/api/life/observers/"+observerID+"/knowledge", map[string]any{"title": "长期计划", "content": "明年初按计划离职", "source": "user"}, map[string]string{"observerId": observerID}, testHandler.AddLifeObserverKnowledge)
	if w.Code != http.StatusCreated {
		t.Fatalf("add observer knowledge: %d %s", w.Code, w.Body.String())
	}
	w = callLifeHandler(t, http.MethodPost, "/api/life/observers/"+observerID+"/run", nil, map[string]string{"observerId": observerID}, testHandler.RunLifeObserver)
	if w.Code != http.StatusAccepted {
		t.Fatalf("run observer: %d %s", w.Code, w.Body.String())
	}

	w = callLifeHandler(t, http.MethodPut, "/api/life/proactive-policy", map[string]any{"enabled": true, "timezone": "Asia/Shanghai", "quiet_hours": map[string]any{"start": "23:00", "end": "08:00"}, "minimum_interval_hours": 24}, nil, testHandler.UpdateLifeProactivePolicy)
	if w.Code != http.StatusOK {
		t.Fatalf("update proactive policy: %d %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_observer WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID)
	})
}

func TestListLifeCognitionJobsReturnsWebContract(t *testing.T) {
	requireHandlerDatabase(t)
	agentID := createHandlerTestAgent(t, "CompleteLifeJobContractCompanion", nil)
	configureLifeCompanionForTest(t, agentID)
	var jobID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO life_cognition_job
			(workspace_id, user_id, companion_agent_id, job_type, dedupe_key, input, scheduled_at)
		VALUES ($1, $2, $3, 'understand_materials', $4, '{"reason":"contract"}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, testUserID, agentID, "contract:"+time.Now().UTC().Format(time.RFC3339Nano)).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_cognition_job WHERE id=$1`, jobID)
	})

	w := callLifeHandler(t, http.MethodGet, "/api/life/cognition-jobs", nil, nil, testHandler.ListLifeCognitionJobs)
	if w.Code != http.StatusOK {
		t.Fatalf("list cognition jobs: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Jobs []struct {
			ID          string          `json:"id"`
			Input       json.RawMessage `json:"input"`
			Output      json.RawMessage `json:"output"`
			ScheduledAt string          `json:"scheduled_at"`
			CompletedAt *string         `json:"completed_at"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, job := range response.Jobs {
		if job.ID != jobID {
			continue
		}
		found = true
		if !containsJSONText(job.Input, "contract") || string(job.Output) != "null" || job.ScheduledAt == "" || job.CompletedAt != nil {
			t.Fatalf("unexpected cognition job contract: %#v body=%s", job, w.Body.String())
		}
	}
	if !found {
		t.Fatalf("created cognition job missing: %s", w.Body.String())
	}
}

func TestGovernedLifeContextUsesBoundedVersionedIndexes(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	createdIDs := make([]string, 0, candidateLifeMemoryIndexLimit+1)
	longContent := strings.Repeat("长期候选认识", 80)
	for index := 0; index < candidateLifeMemoryIndexLimit+1; index++ {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO life_memory (
				workspace_id, user_id, created_by_type, created_by_id, kind,
				content, confidence, urgency, uncertainty, updated_at
			) VALUES ($1, $2, 'member', $2, 'understanding', $3, 0.6, 0.2, $4, $5)
			RETURNING id
		`, testWorkspaceID, testUserID, longContent, strings.Repeat("仍需观察", 40), time.Date(2099, 1, 1, 0, 0, index, 0, time.UTC)).Scan(&id); err != nil {
			t.Fatalf("create context candidate %d: %v", index, err)
		}
		createdIDs = append(createdIDs, id)
	}
	t.Cleanup(func() {
		for _, id := range createdIDs {
			_, _ = testPool.Exec(ctx, `DELETE FROM life_memory WHERE id=$1`, id)
		}
	})

	scope := lifeRequestScope{workspaceID: parseUUID(testWorkspaceID), userID: parseUUID(testUserID)}
	governed, err := testHandler.buildGovernedLifeContext(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	var contextValue struct {
		ContextVersion    string `json:"context_version"`
		CandidateMemories []struct {
			Content     string `json:"content"`
			Uncertainty string `json:"uncertainty"`
		} `json:"candidate_memories_not_facts"`
	}
	if err := json.Unmarshal([]byte(governed), &contextValue); err != nil {
		t.Fatal(err)
	}
	if contextValue.ContextVersion != lifeContextVersion {
		t.Fatalf("unexpected context version: %q", contextValue.ContextVersion)
	}
	if len(contextValue.CandidateMemories) != candidateLifeMemoryIndexLimit {
		t.Fatalf("candidate index must be bounded at %d, got %d", candidateLifeMemoryIndexLimit, len(contextValue.CandidateMemories))
	}
	for _, memory := range contextValue.CandidateMemories {
		if len([]rune(memory.Content)) > 201 || len([]rune(memory.Uncertainty)) > 101 {
			t.Fatalf("candidate index contains unbounded text: %#v", memory)
		}
	}
}

func TestConfirmedModuleProposalBecomesActiveContext(t *testing.T) {
	requireHandlerDatabase(t)
	agentID := createHandlerTestAgent(t, "CompleteLifeModuleCompanion", nil)
	configureLifeCompanionForTest(t, agentID)
	w := callLifeHandler(t, http.MethodPost, "/api/life/proposals", map[string]any{
		"proposal_type": "module_adoption", "title": "启用心情日记", "summary": "从实验沉淀",
		"payload": map[string]any{"module_name": "心情日记", "module_definition": map[string]any{"fields": []string{"事件", "想法", "情绪"}}},
	}, nil, testHandler.CreateLifeActionProposal)
	if w.Code != http.StatusCreated {
		t.Fatalf("create module proposal: %d %s", w.Code, w.Body.String())
	}
	var proposal lifeProposalResponse
	_ = json.Unmarshal(w.Body.Bytes(), &proposal)
	w = callLifeHandler(t, http.MethodPost, "/api/life/proposals/"+proposal.ID+"/confirm", nil, map[string]string{"proposalId": proposal.ID}, testHandler.ConfirmLifeActionProposal)
	if w.Code != http.StatusCreated {
		t.Fatalf("confirm module proposal: %d %s", w.Code, w.Body.String())
	}
	w = callLifeHandler(t, http.MethodGet, "/api/life/modules", nil, nil, testHandler.ListLifeModules)
	if w.Code != http.StatusOK || !containsJSONText(w.Body.Bytes(), "心情日记") || !containsJSONText(w.Body.Bytes(), "active") {
		t.Fatalf("active module missing: %d %s", w.Code, w.Body.String())
	}
	scope := lifeRequestScope{workspaceID: parseUUID(testWorkspaceID), userID: parseUUID(testUserID)}
	governed, err := testHandler.buildGovernedLifeContext(context.Background(), scope)
	if err != nil || !containsJSONText([]byte(governed), "心情日记") {
		t.Fatalf("active module not injected into context: %v %s", err, governed)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_action_proposal WHERE workspace_id=$1 AND user_id=$2 AND companion_agent_id=$3`, testWorkspaceID, testUserID, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_module WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID)
	})
}

func TestConfirmedSharedChangeProposalsExecuteAtomically(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "CompleteLifeSharedChangeCompanion", nil)
	configureLifeCompanionForTest(t, agentID)
	messageID := createLifeChatEvidence(t, agentID, "最近只是一次压力信号，不是离职决定。")
	memory := createLifeMemoryForTest(t, "chat_message", messageID, "understanding", "出现了离职决定")

	createAndConfirm := func(proposalType, title string, payload map[string]any) map[string]any {
		t.Helper()
		w := callLifeHandler(t, http.MethodPost, "/api/life/proposals", map[string]any{"proposal_type": proposalType, "title": title, "summary": "用户确认后原子执行", "payload": payload}, nil, testHandler.CreateLifeActionProposal)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s proposal: %d %s", proposalType, w.Code, w.Body.String())
		}
		var proposal lifeProposalResponse
		if err := json.Unmarshal(w.Body.Bytes(), &proposal); err != nil {
			t.Fatal(err)
		}
		w = callLifeHandler(t, http.MethodPost, "/api/life/proposals/"+proposal.ID+"/confirm", nil, map[string]string{"proposalId": proposal.ID}, testHandler.ConfirmLifeActionProposal)
		if w.Code != http.StatusOK && w.Code != http.StatusCreated {
			t.Fatalf("confirm %s proposal: %d %s", proposalType, w.Code, w.Body.String())
		}
		var result map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result
	}

	confidence, urgency := 0.88, 0.3
	createAndConfirm("memory_change", "纠正离职认识", map[string]any{"memory_id": memory.ID, "memory_action": "correct", "memory_kind": "weak_signal", "memory_content": "这次表达是压力信号，不是离职决定", "memory_confidence": confidence, "memory_urgency": urgency, "memory_uncertainty": "继续结合后续行为判断"})
	var corrected string
	if err := testPool.QueryRow(ctx, `SELECT content FROM life_memory WHERE id=$1`, memory.ID).Scan(&corrected); err != nil || corrected != "这次表达是压力信号，不是离职决定" {
		t.Fatalf("confirmed memory correction missing: %v %q", err, corrected)
	}

	identityResult := createAndConfirm("identity_change", "让搭子更直接", map[string]any{"stable_core": map[string]any{"traits": []string{"直接", "热烈"}}, "relationship_contract": map[string]any{"support_without_control": true}, "growth_profile": map[string]any{"may_grow": []string{"兴趣"}}, "expression_profile": map[string]any{"strong_language_allowed": true}, "interests": []string{"心理学"}, "change_reason": "共同确认表达方式"})
	if identityResult["identity_version_id"] == nil {
		t.Fatalf("identity proposal did not return a receipt: %#v", identityResult)
	}

	projectResult := createAndConfirm("project_create", "创建心理学实验项目", map[string]any{"project_title": "人生实验", "project_description": "承载确认后的心理学实验研发"})
	projectID, _ := projectResult["project_id"].(string)
	if projectID == "" {
		t.Fatalf("project proposal did not create a project: %#v", projectResult)
	}
	actionResult := createAndConfirm("agent_action", "查询下周天气", map[string]any{"action_title": "查询下周天气", "action_instructions": "使用已配置的天气工具查询并把结果发回任务。"})
	actionIssueID, _ := actionResult["issue_id"].(string)
	actionTaskID, _ := actionResult["task_id"].(string)
	if actionIssueID == "" || actionTaskID == "" {
		t.Fatalf("confirmed reality action was not queued: %#v", actionResult)
	}
	var queuedStatus string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id=$1 AND issue_id=$2 AND agent_id=$3`, actionTaskID, actionIssueID, agentID).Scan(&queuedStatus); err != nil || queuedStatus != "queued" {
		t.Fatalf("confirmed reality action task missing: %v %q", err, queuedStatus)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, actionTaskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, actionIssueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM project WHERE id=$1`, projectID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_action_proposal WHERE workspace_id=$1 AND user_id=$2 AND companion_agent_id=$3`, testWorkspaceID, testUserID, agentID)
	})
}

func TestLifeCognitionOutputMaterializesAndProactiveSpeechReachesInbox(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "CompleteLifeCognitionCompanion", nil)
	configureLifeCompanionForTest(t, agentID)
	var materialID string
	if err := testPool.QueryRow(ctx, `INSERT INTO life_material (workspace_id,user_id,source_type,source_key,content,occurred_at) VALUES ($1,$2,'manual',gen_random_uuid()::text,'今天第一次记录心情',now()) RETURNING id`, testWorkspaceID, testUserID).Scan(&materialID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM life_material WHERE id=$1`, materialID) })
	var jobID, taskID string
	if err := testPool.QueryRow(ctx, `INSERT INTO life_cognition_job (workspace_id,user_id,companion_agent_id,job_type,status,dedupe_key,started_at,attempt) VALUES ($1,$2,$3,'understand_materials','running',$4,now(),1) RETURNING id`, testWorkspaceID, testUserID, agentID, "test:"+materialID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	taskContext, _ := json.Marshal(map[string]any{"type": "life_cognition", "input": map[string]any{"new_materials": []map[string]any{{"id": materialID, "content": "今天第一次记录心情"}}}})
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id,runtime_id,status,priority,initiator_user_id,started_at,context) VALUES ($1,$2,'running',0,$3,now(),$4) RETURNING id`, agentID, handlerTestRuntimeID(t), testUserID, taskContext).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, ctx, `UPDATE life_cognition_job SET task_id=$2 WHERE id=$1`, jobID, taskID)
	evidence := []map[string]any{
		{"source_type": "manual", "source_id": materialID, "excerpt": "今天第一次记录心情", "observed_at": time.Now().UTC().Format(time.RFC3339)},
		{"source_type": "manual", "source_id": materialID, "excerpt": "这是同一份材料里的补充证据", "observed_at": time.Now().UTC().Format(time.RFC3339)},
	}
	output := map[string]any{
		"memory_candidates":  []map[string]any{{"kind": "weak_signal", "content": "开始尝试记录心情", "confidence": 0.55, "urgency": 0.2, "uncertainty": "只有一次", "evidence": evidence}},
		"internal_thoughts":  []map[string]any{{"type": "question", "title": "心情记录是否有帮助", "content": "继续观察记录负担", "metadata": map[string]any{}, "evidence": evidence}},
		"action_proposals":   []map[string]any{{"proposal_type": "workspace_issue", "title": "复盘心情记录", "summary": "先由用户确认", "payload": map[string]any{"title": "复盘心情记录"}, "evidence": evidence}},
		"proactive_decision": map[string]any{"status": "spoke", "trigger_source": "manual", "reason": "第一次记录值得温和回应", "message": "记下来了，我们先不用急着得出结论。", "context_snapshot": map[string]any{"material_id": materialID}, "evidence": evidence},
	}
	w := callCompanionAgentHandler(t, taskID, agentID, http.MethodPost, "/api/life/agent/jobs/"+jobID+"/complete", map[string]any{"output": output}, map[string]string{"jobId": jobID}, testHandler.CompleteCompanionCognitionJob)
	if w.Code != http.StatusOK {
		t.Fatalf("complete cognition job: %d %s", w.Code, w.Body.String())
	}
	var memoryID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM life_memory WHERE workspace_id=$1 AND user_id=$2 AND content='开始尝试记录心情'`, testWorkspaceID, testUserID).Scan(&memoryID); err != nil {
		t.Fatal(err)
	}
	var evidenceCount int
	var evidenceExcerpt string
	if err := testPool.QueryRow(ctx, `SELECT count(*), min(excerpt) FROM life_memory_evidence WHERE memory_id=$1`, memoryID).Scan(&evidenceCount, &evidenceExcerpt); err != nil {
		t.Fatal(err)
	}
	if evidenceCount != 1 || !strings.Contains(evidenceExcerpt, "今天第一次记录心情") || !strings.Contains(evidenceExcerpt, "同一份材料里的补充证据") {
		t.Fatalf("coalesced evidence count=%d excerpt=%q", evidenceCount, evidenceExcerpt)
	}
	var inboxCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM inbox_item WHERE workspace_id=$1 AND recipient_id=$2 AND type='life_companion' AND body LIKE '%记下来了%'`, testWorkspaceID, testUserID).Scan(&inboxCount); err != nil {
		t.Fatal(err)
	}
	if inboxCount != 1 {
		t.Fatalf("proactive inbox=%d", inboxCount)
	}

	w = callLifeHandler(t, http.MethodDelete, "/api/life/memories/"+memoryID, nil, map[string]string{"memoryId": memoryID}, testHandler.DeleteLifeMemory)
	if w.Code != http.StatusNoContent {
		t.Fatalf("permanently delete cognition memory: %d %s", w.Code, w.Body.String())
	}
	var leaked int
	if err := testPool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM life_internal_thought WHERE workspace_id=$1 AND user_id=$2 AND title='心情记录是否有帮助') +
			(SELECT count(*) FROM life_action_proposal WHERE workspace_id=$1 AND user_id=$2 AND title='复盘心情记录') +
			(SELECT count(*) FROM life_proactive_check WHERE workspace_id=$1 AND user_id=$2 AND message LIKE '%记下来了%') +
			(SELECT count(*) FROM inbox_item WHERE workspace_id=$1 AND recipient_id=$2 AND type='life_companion' AND body LIKE '%记下来了%')
	`, testWorkspaceID, testUserID).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("derived life records survived permanent deletion: %d", leaked)
	}
	var persistedContext []byte
	var persistedOutput []byte
	if err := testPool.QueryRow(ctx, `SELECT task.context, COALESCE(job.output, '{}'::jsonb) FROM agent_task_queue task JOIN life_cognition_job job ON job.task_id=task.id WHERE task.id=$1`, taskID).Scan(&persistedContext, &persistedOutput); err != nil {
		t.Fatal(err)
	}
	if containsJSONText(persistedContext, "今天第一次记录心情") || containsJSONText(persistedOutput, "今天第一次记录心情") {
		t.Fatalf("background task retained forgotten content: context=%s output=%s", persistedContext, persistedOutput)
	}
	var staleJobID, staleTaskID string
	if err := testPool.QueryRow(ctx, `INSERT INTO life_cognition_job (workspace_id,user_id,companion_agent_id,job_type,status,dedupe_key,started_at,attempt) VALUES ($1,$2,$3,'understand_materials','running',$4,now(),1) RETURNING id`, testWorkspaceID, testUserID, agentID, "stale-test:"+materialID).Scan(&staleJobID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id,runtime_id,status,priority,initiator_user_id,started_at) VALUES ($1,$2,'running',0,$3,now()) RETURNING id`, agentID, handlerTestRuntimeID(t), testUserID).Scan(&staleTaskID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, ctx, `UPDATE life_cognition_job SET task_id=$2 WHERE id=$1`, staleJobID, staleTaskID)
	w = callCompanionAgentHandler(t, staleTaskID, agentID, http.MethodPost, "/api/life/agent/jobs/"+staleJobID+"/complete", map[string]any{"output": output}, map[string]string{"jobId": staleJobID}, testHandler.CompleteCompanionCognitionJob)
	if w.Code != http.StatusOK {
		t.Fatalf("complete stale cognition output after forgetting: %d %s", w.Code, w.Body.String())
	}
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM life_memory WHERE content='开始尝试记录心情') + (SELECT count(*) FROM life_internal_thought WHERE title='心情记录是否有帮助') + (SELECT count(*) FROM life_action_proposal WHERE title='复盘心情记录')`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("forgotten source was regenerated from stale task output: %d", leaked)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE workspace_id=$1 AND recipient_id=$2 AND type='life_companion'`, testWorkspaceID, testUserID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_proactive_check WHERE workspace_id=$1 AND user_id=$2 AND companion_agent_id=$3`, testWorkspaceID, testUserID, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_memory WHERE workspace_id=$1 AND user_id=$2 AND content='开始尝试记录心情'`, testWorkspaceID, testUserID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_cognition_job WHERE id=$1`, jobID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_cognition_job WHERE id=$1`, staleJobID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, staleTaskID)
	})
}

func TestLifeCognitionOutputRejectsFieldsFromAnotherJobType(t *testing.T) {
	err := validateLifeJobOutput("proactive_check", lifeCognitionOutput{
		ProactiveAssessment: &lifeJobProactiveAssessmentOutput{CheckID: "invented"},
	})
	var outputErr lifeJobOutputError
	if !errors.As(err, &outputErr) {
		t.Fatalf("expected governed output error, got %v", err)
	}
	if got := outputErr.Error(); got != "proactive_assessment is not allowed for proactive_check" {
		t.Fatalf("unexpected output error: %q", got)
	}
}

func TestLifeCognitionOutputRejectsProseInTimestampFields(t *testing.T) {
	err := validateLifeJobOutput("understand_materials", lifeCognitionOutput{
		RelationshipEvents: []lifeJobRelationshipOutput{{
			Type: "boundary", Status: "waiting", RevisitAfter: "等用户准备好再回看",
		}},
	})
	var outputErr lifeJobOutputError
	if !errors.As(err, &outputErr) || !strings.Contains(outputErr.Error(), "invalid RFC3339 time") {
		t.Fatalf("expected precise timestamp validation error, got %v", err)
	}
}

func TestLifeCognitionTasksDoNotJoinQuickCreateSerialization(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "ConcurrentLifeCognition", nil)
	runtimeID := handlerTestRuntimeID(t)
	var taskIDs []string
	createTask := func(taskType string) {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id,runtime_id,status,context)
			VALUES ($1,$2,'queued',jsonb_build_object('type',$3::text)) RETURNING id
		`, agentID, runtimeID, taskType).Scan(&id); err != nil {
			t.Fatal(err)
		}
		taskIDs = append(taskIDs, id)
	}
	claim := func() error {
		_, err := testHandler.Queries.ClaimAgentTaskForRuntime(ctx, db.ClaimAgentTaskForRuntimeParams{
			AgentID: parseUUID(agentID), RuntimeID: parseUUID(runtimeID),
		})
		return err
	}

	createTask("life_cognition")
	createTask("life_cognition")
	if err := claim(); err != nil {
		t.Fatalf("claim first life cognition task: %v", err)
	}
	if err := claim(); err != nil {
		t.Fatalf("claim concurrent life cognition task: %v", err)
	}
	mustExec(t, ctx, `UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id = ANY($1::uuid[])`, taskIDs)

	createTask("quick_create")
	createTask("quick_create")
	if err := claim(); err != nil {
		t.Fatalf("claim first quick-create task: %v", err)
	}
	if _, err := testHandler.Queries.ClaimAgentTaskForRuntime(ctx, db.ClaimAgentTaskForRuntimeParams{
		AgentID: parseUUID(agentID), RuntimeID: parseUUID(runtimeID),
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second quick-create claim should remain serialized, got %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = ANY($1::uuid[])`, taskIDs)
	})
}

func TestLifeCognitionTasksYieldToInteractiveChat(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	agentID := parseUUID(createHandlerTestAgent(t, "BackgroundLifeCognition", nil))
	task, err := testHandler.Queries.CreateLifeCognitionAgentTask(ctx, db.CreateLifeCognitionAgentTaskParams{
		AgentID: agentID, RuntimeID: parseUUID(handlerTestRuntimeID(t)), Context: []byte(`{"type":"life_cognition"}`),
		InitiatorUserID: parseUUID(testUserID), TriggerSummary: pgtype.Text{String: "人生后台任务", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, task.ID)
	})
	if task.Priority >= 2 {
		t.Fatalf("life cognition priority %d must remain below interactive chat priority 2", task.Priority)
	}
}

func TestLifeProactiveReviewRecordsValueAndAdjustsRhythm(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "CompleteLifeProactiveReview", nil)
	configureLifeCompanionForTest(t, agentID)
	var checkID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO life_proactive_check (
			workspace_id,user_id,companion_agent_id,status,trigger_source,reason,message,user_responded_at
		) VALUES ($1,$2,$3,'spoke','manual','关心用户','今天还好吗？',now()) RETURNING id
	`, testWorkspaceID, testUserID, agentID).Scan(&checkID); err != nil {
		t.Fatal(err)
	}
	var jobID, taskID string
	if err := testPool.QueryRow(ctx, `INSERT INTO life_cognition_job (workspace_id,user_id,companion_agent_id,job_type,status,dedupe_key,started_at,attempt) VALUES ($1,$2,$3,'proactive_review','running',$4,now(),1) RETURNING id`, testWorkspaceID, testUserID, agentID, "proactive-review-test:"+checkID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id,runtime_id,status,priority,initiator_user_id,started_at) VALUES ($1,$2,'running',0,$3,now()) RETURNING id`, agentID, handlerTestRuntimeID(t), testUserID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, ctx, `UPDATE life_cognition_job SET task_id=$2 WHERE id=$1`, jobID, taskID)
	w := callCompanionAgentHandler(t, taskID, agentID, http.MethodPost, "/api/life/agent/jobs/"+jobID+"/complete", map[string]any{"output": map[string]any{
		"proactive_assessment": map[string]any{"check_id": checkID, "value_assessment": "用户有回应，但语气显示当时更需要安静；这次关心时机偏早。", "minimum_interval_hours": 18},
	}}, map[string]string{"jobId": jobID}, testHandler.CompleteCompanionCognitionJob)
	if w.Code != http.StatusOK {
		t.Fatalf("complete proactive review: %d %s", w.Code, w.Body.String())
	}
	var assessment string
	var intervalHours float64
	if err := testPool.QueryRow(ctx, `
		SELECT c.value_assessment, EXTRACT(EPOCH FROM p.minimum_interval) / 3600
		FROM life_proactive_check c
		JOIN life_proactive_policy p USING (workspace_id,user_id)
		WHERE c.id=$1
	`, checkID).Scan(&assessment, &intervalHours); err != nil {
		t.Fatal(err)
	}
	if assessment == "" || intervalHours != 18 {
		t.Fatalf("proactive review was not persisted: assessment=%q interval=%v", assessment, intervalHours)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM life_cognition_job WHERE id=$1`, jobID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id=$1`, taskID)
		_, _ = testPool.Exec(ctx, `DELETE FROM life_proactive_check WHERE id=$1`, checkID)
	})
}
