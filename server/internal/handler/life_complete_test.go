package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestLifeUpgradeEvaluationResponsePreservesJSON(t *testing.T) {
	row := db.LifeUpgradeEvaluation{Scenarios: []byte(`[{"index":1}]`), Result: []byte(`{"pass_rate":1}`), Status: "passed"}
	raw, err := json.Marshal(lifeUpgradeEvaluationResponse(row))
	if err != nil {
		t.Fatalf("marshal upgrade evaluation response: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode upgrade evaluation response: %v", err)
	}
	if _, ok := response["scenarios"].([]any); !ok {
		t.Fatalf("scenarios encoded as %T: %s", response["scenarios"], raw)
	}
	if _, ok := response["result"].(map[string]any); !ok {
		t.Fatalf("result encoded as %T: %s", response["result"], raw)
	}
}

func TestCoalesceLifeMemoryEvidenceMergesOneSource(t *testing.T) {
	items := coalesceLifeMemoryEvidence([]lifeJobEvidenceOutput{
		{SourceType: "chat_message", SourceID: "source", Excerpt: "first", Stance: "supports"},
		{SourceType: "chat_message", SourceID: "source", Excerpt: "second", Stance: "supports"},
	})
	if len(items) != 1 || !strings.Contains(items[0].Excerpt, "first") || !strings.Contains(items[0].Excerpt, "second") || items[0].Stance != "supports" {
		t.Fatalf("coalesced evidence = %#v", items)
	}
}

func TestLifeCognitionOrdinaryTerminalCallbacksStayAtomic(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "LifeTerminalFence", nil)
	runtimeID := handlerTestRuntimeID(t)

	tests := []struct {
		name string
		call func(pgtype.UUID) error
	}{
		{
			name: "plain completion requires structured output",
			call: func(taskID pgtype.UUID) error {
				_, err := testHandler.TaskService.CompleteTask(ctx, taskID, []byte(`{"output":"plain text"}`), "", "", "", false, "", "")
				if !errors.Is(err, service.ErrLifeStructuredOutputRequired) {
					return fmt.Errorf("plain completion error = %v", err)
				}
				return nil
			},
		},
		{
			name: "daemon failure uses cognition retry only",
			call: func(taskID pgtype.UUID) error {
				_, err := testHandler.TaskService.FailTask(ctx, taskID, "provider timed out", "", "", "", "timeout", false, "", "")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claimToken := uuid.NewString()
			contextVersion := int64(7)
			var jobID, taskID pgtype.UUID
			if err := testPool.QueryRow(ctx, `
				INSERT INTO life_cognition_job (
					workspace_id,user_id,companion_agent_id,job_type,status,dedupe_key,
					started_at,attempt,max_attempts,claim_token,lease_until,context_version
				) VALUES ($1,$2,$3,'understand_materials','running',$4,now(),1,3,$5,now()+interval '10 minutes',$6)
				RETURNING id`, testWorkspaceID, testUserID, agentID, "terminal-fence:"+uuid.NewString(), claimToken, contextVersion).Scan(&jobID); err != nil {
				t.Fatal(err)
			}
			contextJSON, err := json.Marshal(map[string]any{
				"type": "life_cognition", "job_id": uuidToString(jobID),
				"job_type": "understand_materials", "workspace_id": testWorkspaceID,
				"user_id": testUserID, "claim_token": claimToken,
				"context_version_number": contextVersion, "input": map[string]any{},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := testPool.QueryRow(ctx, `
				INSERT INTO agent_task_queue (
					agent_id,runtime_id,status,priority,initiator_user_id,started_at,context
				) VALUES ($1,$2,'running',0,$3,now(),$4) RETURNING id`,
				agentID, runtimeID, testUserID, contextJSON).Scan(&taskID); err != nil {
				t.Fatal(err)
			}
			mustExec(t, ctx, `UPDATE life_cognition_job SET task_id=$2 WHERE id=$1`, jobID, taskID)
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM domain_event_delivery WHERE event_id IN (SELECT id FROM domain_event_outbox WHERE task_id=$1)`, uuidToString(taskID))
				_, _ = testPool.Exec(context.Background(), `DELETE FROM domain_event_outbox WHERE task_id=$1`, uuidToString(taskID))
				_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1 OR parent_task_id=$1`, taskID)
				_, _ = testPool.Exec(context.Background(), `DELETE FROM life_cognition_job WHERE id=$1`, jobID)
			})

			if err := tt.call(taskID); err != nil {
				t.Fatal(err)
			}

			var taskStatus, jobStatus string
			var retainedTaskID pgtype.UUID
			if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id=$1`, taskID).Scan(&taskStatus); err != nil {
				t.Fatal(err)
			}
			if err := testPool.QueryRow(ctx, `SELECT status,task_id FROM life_cognition_job WHERE id=$1`, jobID).Scan(&jobStatus, &retainedTaskID); err != nil {
				t.Fatal(err)
			}
			if taskStatus != "failed" || jobStatus != "failed" || retainedTaskID.Valid {
				t.Fatalf("terminal states task=%q job=%q retained_task=%v", taskStatus, jobStatus, retainedTaskID.Valid)
			}

			var retryTasks, events int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE parent_task_id=$1`, taskID).Scan(&retryTasks); err != nil {
				t.Fatal(err)
			}
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM domain_event_outbox WHERE task_id=$1 AND event_type='task:failed'`, uuidToString(taskID)).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if retryTasks != 0 || events != 1 {
				t.Fatalf("ordinary retry tasks=%d durable events=%d", retryTasks, events)
			}
		})
	}
}

func TestQueueLifeMaterialUnderstandingBatchesExactSources(t *testing.T) {
	requireHandlerDatabase(t)
	companionID := parseUUID(createHandlerTestAgent(t, "BatchLifeMaterials", nil))
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin batch transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	var firstMaterialID, secondMaterialID pgtype.UUID
	for index, destination := range []*pgtype.UUID{&firstMaterialID, &secondMaterialID} {
		err = tx.QueryRow(context.Background(), `
			INSERT INTO life_material (
				workspace_id, user_id, source_type, source_key, source_revision, content, metadata, occurred_at
			) VALUES ($1, $2, 'manual', $3, '1', $4, '{}'::jsonb, now())
			RETURNING id`, testWorkspaceID, testUserID, "batch-material-"+time.Now().Format("150405.000000000")+string(rune('a'+index)), "material").Scan(destination)
		if err != nil {
			t.Fatalf("insert material %d: %v", index, err)
		}
	}
	queries := db.New(tx)
	firstJobID, err := queries.QueueLifeMaterialUnderstanding(context.Background(), db.QueueLifeMaterialUnderstandingParams{
		WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID), CompanionAgentID: companionID, MaterialID: firstMaterialID,
	})
	if err != nil {
		t.Fatalf("queue first material: %v", err)
	}
	secondJobID, err := queries.QueueLifeMaterialUnderstanding(context.Background(), db.QueueLifeMaterialUnderstandingParams{
		WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID), CompanionAgentID: companionID, MaterialID: secondMaterialID,
	})
	if err != nil {
		t.Fatalf("queue second material: %v", err)
	}
	if firstJobID != secondJobID {
		t.Fatalf("same-minute materials created different jobs: %v != %v", firstJobID, secondJobID)
	}
	var input []byte
	var secondsUntilDue float64
	if err := tx.QueryRow(context.Background(), `
		SELECT input, extract(epoch FROM (scheduled_at - now()))
		FROM life_cognition_job WHERE id=$1`, firstJobID).Scan(&input, &secondsUntilDue); err != nil {
		t.Fatalf("read material batch: %v", err)
	}
	var payload struct {
		MaterialIDs       []string `json:"material_ids"`
		ProcessingCursors []string `json:"processing_cursors"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		t.Fatalf("decode material batch: %v", err)
	}
	if len(payload.MaterialIDs) != 2 || len(payload.ProcessingCursors) != 2 {
		t.Fatalf("batch input = %s", input)
	}
	if secondsUntilDue <= 0 || secondsUntilDue > 65 {
		t.Fatalf("batch due in %.3fs, want (0,65]", secondsUntilDue)
	}
	if err := queries.ScrubLifeCognitionTasksByMaterialIDs(context.Background(), db.ScrubLifeCognitionTasksByMaterialIDsParams{
		WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID), Column3: []string{uuidToString(firstMaterialID)},
	}); err != nil {
		t.Fatalf("forget first queued material: %v", err)
	}
	if err := tx.QueryRow(context.Background(), `SELECT input FROM life_cognition_job WHERE id=$1`, firstJobID).Scan(&input); err != nil {
		t.Fatalf("read reduced material batch: %v", err)
	}
	if err := json.Unmarshal(input, &payload); err != nil || len(payload.MaterialIDs) != 1 || payload.MaterialIDs[0] != uuidToString(secondMaterialID) {
		t.Fatalf("reduced batch input = %s, error=%v", input, err)
	}
	if err := queries.ScrubLifeCognitionTasksByMaterialIDs(context.Background(), db.ScrubLifeCognitionTasksByMaterialIDsParams{
		WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID), Column3: []string{uuidToString(secondMaterialID)},
	}); err != nil {
		t.Fatalf("forget final queued material: %v", err)
	}
	var remainingJobs int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM life_cognition_job WHERE id=$1`, firstJobID).Scan(&remainingJobs); err != nil || remainingJobs != 0 {
		t.Fatalf("empty material batch survived: count=%d error=%v", remainingJobs, err)
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
	var lastRunAt, nextRunAt time.Time
	if err := testPool.QueryRow(context.Background(), `SELECT last_run_at, next_run_at FROM life_observer WHERE id=$1`, observerID).Scan(&lastRunAt, &nextRunAt); err != nil {
		t.Fatalf("read observer schedule after manual run: %v", err)
	}
	if !nextRunAt.After(lastRunAt) || !nextRunAt.After(time.Now()) {
		t.Fatalf("manual run must advance observer schedule: last=%s next=%s", lastRunAt, nextRunAt)
	}

	if _, err := testPool.Exec(context.Background(), `DELETE FROM life_proactive_policy WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, testUserID); err != nil {
		t.Fatalf("delete proactive policy: %v", err)
	}
	w = callLifeHandler(t, http.MethodGet, "/api/life/proactive-policy", nil, nil, testHandler.GetLifeProactivePolicy)
	if w.Code != http.StatusOK {
		t.Fatalf("get default proactive policy: %d %s", w.Code, w.Body.String())
	}
	var defaultPolicy map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &defaultPolicy); err != nil {
		t.Fatalf("decode default proactive policy: %v", err)
	}
	if enabled, _ := defaultPolicy["enabled"].(bool); enabled {
		t.Fatalf("default proactive policy must be disabled: %s", w.Body.String())
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
			Attempt     int32           `json:"attempt"`
			MaxAttempts int32           `json:"max_attempts"`
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
		if !containsJSONText(job.Input, "contract") || string(job.Output) != "null" || job.ScheduledAt == "" || job.CompletedAt != nil || job.Attempt != 0 || job.MaxAttempts != 3 {
			t.Fatalf("unexpected cognition job contract: %#v body=%s", job, w.Body.String())
		}
	}
	if !found {
		t.Fatalf("created cognition job missing: %s", w.Body.String())
	}
}

func TestListLifeCognitionJobsClientCanceledReturns499(t *testing.T) {
	req := newRequest(http.MethodGet, "/api/life/cognition-jobs", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	newCanceledLifeReadHandler().ListLifeCognitionJobs(w, req)
	if w.Code != 499 {
		t.Fatalf("expected 499 for canceled cognition job read, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCancelledLifeCognitionJobCanBeExplicitlyRetried(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "RetryLifeJobCompanion", nil)
	var jobID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO life_cognition_job (
			workspace_id,user_id,companion_agent_id,job_type,dedupe_key,input,status,
			attempt,max_attempts,error,scheduled_at,started_at,completed_at
		) VALUES ($1,$2,$3,'understand_materials',$4,'{}'::jsonb,'cancelled',3,3,'timeout',now(),now(),now())
		RETURNING id
	`, testWorkspaceID, testUserID, agentID, "retry-cancelled:"+time.Now().UTC().Format(time.RFC3339Nano)).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM life_cognition_job WHERE id=$1`, jobID) })

	w := callLifeHandler(t, http.MethodPost, "/api/life/cognition-jobs/"+jobID+"/retry", nil, map[string]string{"jobId": jobID}, testHandler.RetryLifeCognitionJob)
	if w.Code != http.StatusAccepted {
		t.Fatalf("retry cancelled cognition job: %d %s", w.Code, w.Body.String())
	}
	var status string
	var attempt int32
	var taskID pgtype.UUID
	var jobError string
	if err := testPool.QueryRow(ctx, `SELECT status,attempt,task_id,error FROM life_cognition_job WHERE id=$1`, jobID).Scan(&status, &attempt, &taskID, &jobError); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || attempt != 0 || taskID.Valid || jobError != "" {
		t.Fatalf("unexpected retried job state status=%s attempt=%d task=%v error=%q", status, attempt, taskID, jobError)
	}
	w = callLifeHandler(t, http.MethodPost, "/api/life/cognition-jobs/"+jobID+"/retry", nil, map[string]string{"jobId": jobID}, testHandler.RetryLifeCognitionJob)
	if w.Code != http.StatusConflict {
		t.Fatalf("queued cognition job must not be retried again: %d %s", w.Code, w.Body.String())
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
		if len([]rune(memory.Content)) > 61 || len([]rune(memory.Uncertainty)) > 41 {
			t.Fatalf("candidate index contains unbounded text: %#v", memory)
		}
	}
}

func TestCurrentLifeJobInputRefreshesContextVersion(t *testing.T) {
	input, err := currentLifeJobInput(json.RawMessage(`{"context_version":"life-context-v1","processing_cursor":"cursor-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(input, &value); err != nil {
		t.Fatal(err)
	}
	if value["context_version"] != lifeContextVersion || value["processing_cursor"] != "cursor-1" {
		t.Fatalf("unexpected refreshed life input: %#v", value)
	}
	if _, err := currentLifeJobInput(json.RawMessage(`[]`)); err == nil {
		t.Fatal("non-object life input must fail closed")
	}
}

func TestLifeJobContextIncludesBoundedActiveInternalThoughts(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "BoundedInternalThoughtContext", nil)
	configureLifeCompanionForTest(t, agentID)
	for index := 0; index < lifeInternalThoughtIndexLimit+1; index++ {
		mustExec(t, ctx, `
			INSERT INTO life_internal_thought (
				workspace_id,user_id,companion_agent_id,thought_type,title,content,status,metadata,last_developed_at
			) VALUES ($1,$2,$3,'draft',$4,$5,'active','{}'::jsonb,$6)
		`, testWorkspaceID, testUserID, agentID, fmt.Sprintf("索引想法-%02d", index), "用于验证有界上下文", time.Date(2099, 1, 1, 0, 0, index, 0, time.UTC))
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_internal_thought WHERE workspace_id=$1 AND user_id=$2 AND companion_agent_id=$3`, testWorkspaceID, testUserID, agentID)
	})
	scope := lifeRequestScope{workspaceID: parseUUID(testWorkspaceID), userID: parseUUID(testUserID)}
	contextJSON, err := testHandler.buildLifeJobContext(ctx, scope, "understand_materials", parseUUID(agentID), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Thoughts []struct {
			ID string `json:"id"`
		} `json:"agent_internal_thoughts"`
	}
	if err := json.Unmarshal([]byte(contextJSON), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Thoughts) != lifeInternalThoughtIndexLimit {
		t.Fatalf("internal thought index must be bounded at %d, got %d", lifeInternalThoughtIndexLimit, len(value.Thoughts))
	}
	for _, thought := range value.Thoughts {
		if _, err := uuid.Parse(thought.ID); err != nil {
			t.Fatalf("internal thought index omitted a usable id: %#v", thought)
		}
	}
}

func TestObservationAggregationSelectsOnlyItsNewJudgements(t *testing.T) {
	first := uuid.NewString()
	second := uuid.NewString()
	selected := lifeObservationJudgementIDs(json.RawMessage(fmt.Sprintf(`{"new_judgement_ids":[%q,"invalid",%q,%q]}`, first, second, first)))
	if len(selected) != 2 {
		t.Fatalf("selected judgement ids = %d, want 2", len(selected))
	}
	if _, ok := selected[first]; !ok {
		t.Fatalf("first judgement missing: %#v", selected)
	}
	if _, ok := selected[second]; !ok {
		t.Fatalf("second judgement missing: %#v", selected)
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
	var jobID, taskID, revisionJobID, revisionTaskID, protectedJobID, protectedTaskID, staleJobID, staleTaskID string
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE workspace_id=$1 AND recipient_id=$2 AND type='life_companion'`, testWorkspaceID, testUserID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_proactive_check WHERE workspace_id=$1 AND user_id=$2 AND companion_agent_id=$3`, testWorkspaceID, testUserID, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_action_proposal WHERE workspace_id=$1 AND user_id=$2 AND companion_agent_id=$3`, testWorkspaceID, testUserID, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM life_memory WHERE workspace_id=$1 AND user_id=$2 AND created_by_id=$3`, testWorkspaceID, testUserID, agentID)
		for _, id := range []string{jobID, revisionJobID, protectedJobID, staleJobID} {
			if id != "" {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM life_cognition_job WHERE id=$1`, id)
			}
		}
		for _, id := range []string{taskID, revisionTaskID, protectedTaskID, staleTaskID} {
			if id != "" {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1`, id)
			}
		}
	})
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
	var taskStatus string
	var taskResult []byte
	if err := testPool.QueryRow(ctx, `SELECT status,result FROM agent_task_queue WHERE id=$1`, taskID).Scan(&taskStatus, &taskResult); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "completed" || !containsJSONText(taskResult, "开始尝试记录心情") {
		t.Fatalf("life task status=%q result=%s", taskStatus, taskResult)
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
	var thoughtID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM life_internal_thought WHERE workspace_id=$1 AND user_id=$2 AND companion_agent_id=$3 AND title='心情记录是否有帮助'`, testWorkspaceID, testUserID, agentID).Scan(&thoughtID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO life_cognition_job (workspace_id,user_id,companion_agent_id,job_type,status,dedupe_key,started_at,attempt) VALUES ($1,$2,$3,'understand_materials','running',$4,now(),1) RETURNING id`, testWorkspaceID, testUserID, agentID, "revision-test:"+materialID).Scan(&revisionJobID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id,runtime_id,status,priority,initiator_user_id,started_at,context) VALUES ($1,$2,'running',0,$3,now(),$4) RETURNING id`, agentID, handlerTestRuntimeID(t), testUserID, taskContext).Scan(&revisionTaskID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, ctx, `UPDATE life_cognition_job SET task_id=$2 WHERE id=$1`, revisionJobID, revisionTaskID)
	revisionOutput := map[string]any{
		"memory_candidates": []map[string]any{{"memory_id": memoryID, "kind": "understanding", "content": "心情记录可能有助于看见压力变化", "confidence": 0.68, "urgency": 0.2, "uncertainty": "仍需跨周期观察", "evidence": evidence}},
		"internal_thoughts": []map[string]any{{"thought_id": thoughtID, "type": "question", "title": "心情记录是否有帮助", "content": "这个旧问题已被新证据取代", "status": "archived", "metadata": map[string]any{}, "evidence": evidence}},
	}
	w = callCompanionAgentHandler(t, revisionTaskID, agentID, http.MethodPost, "/api/life/agent/jobs/"+revisionJobID+"/complete", map[string]any{"output": revisionOutput}, map[string]string{"jobId": revisionJobID}, testHandler.CompleteCompanionCognitionJob)
	if w.Code != http.StatusOK {
		t.Fatalf("revise candidate memory: %d %s", w.Code, w.Body.String())
	}
	var memoryCount, revisionCount int
	var revisedContent, revisedStatus string
	if err := testPool.QueryRow(ctx, `SELECT count(*), min(content), min(status) FROM life_memory WHERE workspace_id=$1 AND user_id=$2 AND id=$3`, testWorkspaceID, testUserID, memoryID).Scan(&memoryCount, &revisedContent, &revisedStatus); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM life_memory_revision WHERE memory_id=$1`, memoryID).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if memoryCount != 1 || revisedContent != "心情记录可能有助于看见压力变化" || revisedStatus != "candidate" || revisionCount != 2 {
		t.Fatalf("candidate revision count=%d content=%q status=%q revisions=%d", memoryCount, revisedContent, revisedStatus, revisionCount)
	}
	var thoughtStatus, thoughtContent string
	if err := testPool.QueryRow(ctx, `SELECT status,content FROM life_internal_thought WHERE id=$1`, thoughtID).Scan(&thoughtStatus, &thoughtContent); err != nil {
		t.Fatal(err)
	}
	if thoughtStatus != "archived" || thoughtContent != "这个旧问题已被新证据取代" {
		t.Fatalf("internal thought revision status=%q content=%q", thoughtStatus, thoughtContent)
	}
	w = callLifeHandler(t, http.MethodPost, "/api/life/memories/"+memoryID+"/confirm", nil, map[string]string{"memoryId": memoryID}, testHandler.ConfirmLifeMemory)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm revised candidate: %d %s", w.Code, w.Body.String())
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO life_cognition_job (workspace_id,user_id,companion_agent_id,job_type,status,dedupe_key,started_at,attempt) VALUES ($1,$2,$3,'understand_materials','running',$4,now(),1) RETURNING id`, testWorkspaceID, testUserID, agentID, "protected-test:"+materialID).Scan(&protectedJobID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id,runtime_id,status,priority,initiator_user_id,started_at,context) VALUES ($1,$2,'running',0,$3,now(),$4) RETURNING id`, agentID, handlerTestRuntimeID(t), testUserID, taskContext).Scan(&protectedTaskID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, ctx, `UPDATE life_cognition_job SET task_id=$2 WHERE id=$1`, protectedJobID, protectedTaskID)
	protectedOutput := map[string]any{"memory_candidates": []map[string]any{{"memory_id": memoryID, "kind": "fact", "content": "后台不应覆盖这条已确认记忆", "confidence": 0.99, "urgency": 0.9, "uncertainty": "", "evidence": evidence}}}
	w = callCompanionAgentHandler(t, protectedTaskID, agentID, http.MethodPost, "/api/life/agent/jobs/"+protectedJobID+"/complete", map[string]any{"output": protectedOutput}, map[string]string{"jobId": protectedJobID}, testHandler.CompleteCompanionCognitionJob)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "not available for background revision") {
		t.Fatalf("background revision of governed memory: %d %s", w.Code, w.Body.String())
	}
	if err := testPool.QueryRow(ctx, `SELECT content,status FROM life_memory WHERE id=$1`, memoryID).Scan(&revisedContent, &revisedStatus); err != nil {
		t.Fatal(err)
	}
	if revisedContent != "心情记录可能有助于看见压力变化" || revisedStatus != "confirmed" {
		t.Fatalf("governed memory changed in background: content=%q status=%q", revisedContent, revisedStatus)
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

func TestLifeCognitionOutputRejectsMissingJSONObjects(t *testing.T) {
	tests := []struct {
		name    string
		jobType string
		output  lifeCognitionOutput
	}{
		{name: "internal thought metadata", jobType: "understand_materials", output: lifeCognitionOutput{InternalThoughts: []lifeJobThoughtOutput{{Type: "draft", Title: "回看", Content: "继续观察"}}}},
		{name: "proactive context", jobType: "proactive_check", output: lifeCognitionOutput{ProactiveDecision: &lifeJobProactiveOutput{Status: "silent", TriggerSource: "manual", Reason: "等待用户"}}},
		{name: "experiment module proposal", jobType: "experiment_check", output: lifeCognitionOutput{ExperimentReview: &lifeJobExperimentReviewOutput{RoundID: uuid.NewString()}}},
		{name: "upgrade result", jobType: "upgrade_evaluation", output: lifeCognitionOutput{UpgradeEvaluation: &lifeJobUpgradeEvaluationOutput{EvaluationID: uuid.NewString(), Status: "unknown"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var outputErr lifeJobOutputError
			if err := validateLifeJobOutput(test.jobType, test.output); !errors.As(err, &outputErr) {
				t.Fatalf("expected governed output error, got %v", err)
			}
		})
	}
}

func TestLifeCognitionOutputRejectsIncompleteExperimentReview(t *testing.T) {
	valid := lifeJobExperimentReviewOutput{
		RoundID: uuid.NewString(), Outcome: "结果", Feelings: "感受", Burden: "负担",
		CompanionCorrection: "搭子下次少催促", ModuleProposal: map[string]any{},
	}
	tests := []struct {
		name   string
		mutate func(*lifeJobExperimentReviewOutput)
	}{
		{name: "outcome", mutate: func(review *lifeJobExperimentReviewOutput) { review.Outcome = " " }},
		{name: "feelings", mutate: func(review *lifeJobExperimentReviewOutput) { review.Feelings = " " }},
		{name: "burden", mutate: func(review *lifeJobExperimentReviewOutput) { review.Burden = " " }},
		{name: "companion correction", mutate: func(review *lifeJobExperimentReviewOutput) { review.CompanionCorrection = " " }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			review := valid
			test.mutate(&review)
			var outputErr lifeJobOutputError
			if err := validateLifeJobOutput("experiment_check", lifeCognitionOutput{ExperimentReview: &review}); !errors.As(err, &outputErr) {
				t.Fatalf("expected governed output error, got %v", err)
			}
		})
	}
}

func TestLifeCognitionOutputRejectsInvalidInternalThoughtRevision(t *testing.T) {
	for _, thought := range []lifeJobThoughtOutput{
		{ID: "not-a-uuid", Type: "draft", Title: "回看", Content: "继续观察", Status: "active", Metadata: map[string]any{}},
		{ID: uuid.NewString(), Type: "draft", Title: "回看", Content: "继续观察", Status: "deleted", Metadata: map[string]any{}},
	} {
		var outputErr lifeJobOutputError
		if err := validateLifeJobOutput("understand_materials", lifeCognitionOutput{InternalThoughts: []lifeJobThoughtOutput{thought}}); !errors.As(err, &outputErr) {
			t.Fatalf("expected governed internal thought revision error, got %v", err)
		}
	}
}

func TestObserverOutputAcceptsGovernedKnowledgeAndChronicleEvidence(t *testing.T) {
	err := validateLifeJobOutput("observer_run", lifeCognitionOutput{
		ObserverJudgements: []lifeJobObserverJudgementOutput{{
			Status: "published", Title: "独立判断", Content: "结合观察者资料和编年史", Confidence: 0.7,
			Evidence: []lifeJobEvidenceOutput{
				{SourceType: "observer_knowledge", SourceID: uuid.NewString(), Stance: "context"},
				{SourceType: "chronicle", SourceID: uuid.NewString(), Stance: "supports"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("governed observer evidence was rejected: %v", err)
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
		_, err := testHandler.Queries.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
			AgentID: parseUUID(agentID), RuntimeID: parseUUID(runtimeID),
			PrepareLeaseSecs: 60, RuntimeStaleSecs: 300,
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
	if _, err := testHandler.Queries.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
		AgentID: parseUUID(agentID), RuntimeID: parseUUID(runtimeID),
		PrepareLeaseSecs: 60, RuntimeStaleSecs: 300,
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
