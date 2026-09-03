package handler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestDeleteWorkspaceLifeDataRemovesTheWholeGraph exercises the application
// owned Life teardown directly. Life tables intentionally have no foreign
// keys, so this regression must prove that an orphaned child cannot survive a
// workspace delete merely because PostgreSQL has nothing left to cascade.
func TestDeleteWorkspaceLifeDataRemovesTheWholeGraph(t *testing.T) {
	requireHandlerDatabase(t)
	ctx := context.Background()
	workspaceID := uuid.NewString()
	otherWorkspaceID := uuid.NewString()
	userID := uuid.NewString()
	agentID := uuid.NewString()
	now := time.Now().UTC()

	insert := func(query string, args ...any) {
		t.Helper()
		if _, err := testPool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed Life fixture: %v", err)
		}
	}
	// Keep the fixture isolated and remove any rows that do not belong to the
	// Life graph after the assertions, in case a future schema adds a trigger.
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM life_context_state WHERE workspace_id IN ($1, $2)`, workspaceID, otherWorkspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM life_material WHERE workspace_id IN ($1, $2)`, workspaceID, otherWorkspaceID)
	})

	identityID := uuid.NewString()
	memoryID := uuid.NewString()
	evidenceSourceID := uuid.NewString()
	otherMemoryID := uuid.NewString()
	experimentID := uuid.NewString()
	roundID := uuid.NewString()
	moduleID := uuid.NewString()
	observerID := uuid.NewString()
	judgementID := uuid.NewString()
	topicID := uuid.NewString()
	observationTopicID := uuid.NewString()
	entryID := uuid.NewString()

	insert(`INSERT INTO companion_profile (workspace_id, user_id, agent_id) VALUES ($1, $2, $3)`, workspaceID, userID, agentID)
	insert(`INSERT INTO life_identity_version (id, workspace_id, user_id, version, status, confirmed_at, confirmed_by_id) VALUES ($1, $2, $3, 1, 'active', $4, $3)`, identityID, workspaceID, userID, now)
	insert(`INSERT INTO life_memory (id, workspace_id, user_id, created_by_type, created_by_id, kind, status, content, confidence, urgency, confirmed_at, confirmed_by_id) VALUES ($1, $2, $3, 'member', $3, 'fact', 'confirmed', 'fixture memory', .8, .1, $4, $3)`, memoryID, workspaceID, userID, now)
	insert(`INSERT INTO life_memory (id, workspace_id, user_id, created_by_type, created_by_id, kind, status, content, confidence, urgency, confirmed_at, confirmed_by_id) VALUES ($1, $2, $3, 'member', $3, 'fact', 'confirmed', 'neighbour memory', .8, .1, $4, $3)`, otherMemoryID, otherWorkspaceID, userID, now)
	insert(`INSERT INTO life_memory_evidence (memory_id, source_type, source_id, observed_at) VALUES ($1, 'manual', $2, $3)`, memoryID, evidenceSourceID, now)
	insert(`INSERT INTO life_memory_dependency (source_memory_id, derived_memory_id) VALUES ($1, $2)`, memoryID, otherMemoryID)
	insert(`INSERT INTO life_memory_revision (memory_id, revision, kind, status, content, confidence, urgency, uncertainty, change_type, changed_by_type, changed_by_id) VALUES ($1, 1, 'fact', 'confirmed', 'fixture memory', .8, .1, '', 'created', 'member', $2)`, memoryID, userID)
	insert(`INSERT INTO life_action_proposal (workspace_id, user_id, companion_agent_id, proposal_type, title) VALUES ($1, $2, $3, 'agent_action', 'fixture proposal')`, workspaceID, userID, agentID)
	insert(`INSERT INTO life_experiment (id, workspace_id, user_id, title, problem, hypothesis, method, created_by_type, created_by_id) VALUES ($1, $2, $3, 'fixture experiment', 'problem', 'hypothesis', '{}', 'member', $3)`, experimentID, workspaceID, userID)
	insert(`INSERT INTO life_experiment_round (id, experiment_id, status, plan) VALUES ($1, $2, 'draft', '{}')`, roundID, experimentID)
	insert(`INSERT INTO life_experiment_memory (round_id, memory_id, role) VALUES ($1, $2, 'input')`, roundID, memoryID)
	insert(`INSERT INTO life_experiment_observation (round_id, material_id, observation_type, content, captured_by, observed_at) VALUES ($1, $2, 'user_checkin', 'fixture observation', 'user', $3)`, roundID, evidenceSourceID, now)
	insert(`INSERT INTO life_module (id, workspace_id, user_id, name) VALUES ($1, $2, $3, 'fixture module')`, moduleID, workspaceID, userID)
	insert(`INSERT INTO life_module_version (module_id, version, definition, confirmed_at, confirmed_by_id) VALUES ($1, 1, '{}', $2, $3)`, moduleID, now, userID)
	insert(`INSERT INTO life_observer (id, workspace_id, user_id, agent_id, name, basis_type) VALUES ($1, $2, $3, $4, 'fixture observer', 'virtual')`, observerID, workspaceID, userID, uuid.NewString())
	insert(`INSERT INTO life_observer_version (observer_id, version, confirmed_at, confirmed_by_id) VALUES ($1, 1, $2, $3)`, observerID, now, userID)
	insert(`INSERT INTO life_observer_knowledge (observer_id, title, content) VALUES ($1, 'fixture knowledge', 'content')`, observerID)
	insert(`INSERT INTO life_observer_judgement (id, observer_id, title, content, evidence, confidence) VALUES ($1, $2, 'fixture judgement', 'content', '[]', .7)`, judgementID, observerID)
	insert(`INSERT INTO life_topic (id, workspace_id, user_id, title, first_observed_at, last_observed_at) VALUES ($1, $2, $3, 'fixture topic', $4, $4)`, topicID, workspaceID, userID, now)
	insert(`INSERT INTO life_topic_memory (topic_id, memory_id, relation) VALUES ($1, $2, 'supports')`, topicID, memoryID)
	insert(`INSERT INTO life_commitment (workspace_id, user_id, content) VALUES ($1, $2, 'fixture commitment')`, workspaceID, userID)
	insert(`INSERT INTO life_internal_thought (workspace_id, user_id, companion_agent_id, thought_type, title, content) VALUES ($1, $2, $3, 'question', 'fixture thought', 'content')`, workspaceID, userID, agentID)
	insert(`INSERT INTO life_cognition_job (workspace_id, user_id, companion_agent_id, job_type, dedupe_key) VALUES ($1, $2, $3, 'understand_materials', $4)`, workspaceID, userID, agentID, uuid.NewString())
	insert(`INSERT INTO life_proactive_policy (workspace_id, user_id) VALUES ($1, $2)`, workspaceID, userID)
	insert(`INSERT INTO life_proactive_check (workspace_id, user_id, companion_agent_id, status, trigger_source, reason) VALUES ($1, $2, $3, 'silent', 'manual', 'fixture check')`, workspaceID, userID, agentID)
	insert(`INSERT INTO life_relationship_event (workspace_id, user_id, event_type) VALUES ($1, $2, 'agreement')`, workspaceID, userID)
	insert(`INSERT INTO life_material (workspace_id, user_id, source_type, source_key, content, occurred_at) VALUES ($1, $2, 'manual', $3, 'fixture material', $4)`, workspaceID, userID, uuid.NewString(), now)
	insert(`INSERT INTO life_forget_tombstone (workspace_id, user_id, source_type, source_key, content_hash) VALUES ($1, $2, 'manual', $3, 'fixture-hash')`, workspaceID, userID, uuid.NewString())
	insert(`INSERT INTO life_observation_topic (id, workspace_id, user_id, title) VALUES ($1, $2, $3, 'fixture observation topic')`, observationTopicID, workspaceID, userID)
	insert(`INSERT INTO life_observation_topic_judgement (topic_id, judgement_id) VALUES ($1, $2)`, observationTopicID, judgementID)
	insert(`INSERT INTO life_chronicle_entry (id, workspace_id, user_id, period_start, period_end, facts) VALUES ($1, $2, $3, $4, $4, 'fixture facts')`, entryID, workspaceID, userID, now)
	insert(`INSERT INTO life_chronicle_evidence (entry_id, source_type, source_id) VALUES ($1, 'manual', $2)`, entryID, evidenceSourceID)
	insert(`INSERT INTO life_chronicle_revision (entry_id, revision, facts, feelings, understanding_then, understanding_later) VALUES ($1, 1, 'facts', '', '', '')`, entryID)
	insert(`INSERT INTO life_upgrade_evaluation (workspace_id, user_id, candidate_label, baseline_label, scenarios) VALUES ($1, $2, 'candidate', 'baseline', '[]')`, workspaceID, userID)
	insert(`INSERT INTO life_chronicle_cursor (workspace_id, user_id, period_kind, next_period_start) VALUES ($1, $2, 'day', $3)`, workspaceID, userID, now)
	insert(`INSERT INTO life_context_state (workspace_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, workspaceID, userID)

	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Life teardown: %v", err)
	}
	q := db.New(tx)
	if err := q.DeleteWorkspaceLifeData(ctx, parseUUID(workspaceID)); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("delete Life graph: %v", err)
	}
	if err := q.DeleteWorkspaceLifeContextState(ctx, parseUUID(workspaceID)); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("delete Life context state: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit Life teardown: %v", err)
	}

	workspaceTables := []string{
		"companion_profile", "life_identity_version", "life_relationship_event", "life_material", "life_derivation", "life_forget_tombstone",
		"life_memory", "life_action_proposal", "life_experiment", "life_proactive_check", "life_chronicle_entry", "life_commitment",
		"life_internal_thought", "life_cognition_job", "life_proactive_policy", "life_module", "life_observer", "life_observation_topic",
		"life_upgrade_evaluation", "life_chronicle_cursor", "life_context_state", "life_topic",
	}
	for _, table := range workspaceTables {
		var count int
		if err := testPool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE workspace_id = $1", workspaceID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s still has %d rows for deleted workspace", table, count)
		}
	}
	var neighbourCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM life_memory WHERE id = $1`, otherMemoryID).Scan(&neighbourCount); err != nil {
		t.Fatalf("count neighbour memory: %v", err)
	}
	if neighbourCount != 1 {
		t.Fatalf("neighbour memory count = %d, want 1", neighbourCount)
	}

	childChecks := []struct {
		name  string
		query string
		args  []any
	}{
		{"life_memory_evidence", `SELECT count(*) FROM life_memory_evidence WHERE memory_id = $1`, []any{memoryID}},
		{"life_memory_dependency", `SELECT count(*) FROM life_memory_dependency WHERE source_memory_id = $1`, []any{memoryID}},
		{"life_memory_revision", `SELECT count(*) FROM life_memory_revision WHERE memory_id = $1`, []any{memoryID}},
		{"life_experiment_round", `SELECT count(*) FROM life_experiment_round WHERE id = $1`, []any{roundID}},
		{"life_experiment_memory", `SELECT count(*) FROM life_experiment_memory WHERE round_id = $1`, []any{roundID}},
		{"life_experiment_observation", `SELECT count(*) FROM life_experiment_observation WHERE round_id = $1`, []any{roundID}},
		{"life_module_version", `SELECT count(*) FROM life_module_version WHERE module_id = $1`, []any{moduleID}},
		{"life_observer_version", `SELECT count(*) FROM life_observer_version WHERE observer_id = $1`, []any{observerID}},
		{"life_observer_knowledge", `SELECT count(*) FROM life_observer_knowledge WHERE observer_id = $1`, []any{observerID}},
		{"life_observer_judgement", `SELECT count(*) FROM life_observer_judgement WHERE id = $1`, []any{judgementID}},
		{"life_topic_memory", `SELECT count(*) FROM life_topic_memory WHERE topic_id = $1`, []any{topicID}},
		{"life_observation_topic_judgement", `SELECT count(*) FROM life_observation_topic_judgement WHERE topic_id = $1`, []any{observationTopicID}},
		{"life_chronicle_evidence", `SELECT count(*) FROM life_chronicle_evidence WHERE entry_id = $1`, []any{entryID}},
		{"life_chronicle_revision", `SELECT count(*) FROM life_chronicle_revision WHERE entry_id = $1`, []any{entryID}},
	}
	for _, check := range childChecks {
		var count int
		if err := testPool.QueryRow(ctx, check.query, check.args...).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", check.name, err)
		}
		if count != 0 {
			t.Errorf("%s still has %d rows", check.name, count)
		}
	}
}
