package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
)

// These tests preserve the Runtime deletion order required by the restrictive
// Agent and Squad foreign keys.

// seedIsolatedRuntime creates an independently disposable Runtime.
func seedIsolatedRuntime(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, NULL, $2, 'cloud', 'isolated_test', 'online', 'isolated test runtime', '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, name).Scan(&runtimeID); err != nil {
		t.Fatalf("seed runtime %q: %v", name, err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, `DELETE FROM agent WHERE runtime_id = $1`, runtimeID)
		mustExec(t, ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})
	return runtimeID
}

// seedAgentOnRuntime creates an agent on the given runtime. If archived is
// true the row is created with archived_at = now().
func seedAgentOnRuntime(t *testing.T, runtimeID, name string, archived bool) string {
	t.Helper()
	ctx := context.Background()
	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, scope, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 1, $4)
		RETURNING id
	`, testWorkspaceID, name, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent %q: %v", name, err)
	}
	if archived {
		if _, err := testPool.Exec(ctx,
			`UPDATE agent SET archived_at = now(), archived_by = $1 WHERE id = $2`,
			testUserID, agentID,
		); err != nil {
			t.Fatalf("archive agent %q: %v", name, err)
		}
	}
	t.Cleanup(func() {
		mustExec(t, ctx, `DELETE FROM squad WHERE leader_id = $1`, agentID)
		mustExec(t, ctx, `DELETE FROM agent WHERE id = $1`, agentID)
	})
	return agentID
}

// seedSquad creates a Squad and optionally archives it.
func seedSquad(t *testing.T, leaderID, name string, archived bool) string {
	t.Helper()
	ctx := context.Background()
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, $2, '', $3, $4)
		RETURNING id
	`, testWorkspaceID, name, leaderID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("seed squad %q: %v", name, err)
	}
	if archived {
		if _, err := testPool.Exec(ctx,
			`UPDATE squad SET archived_at = now(), archived_by = $1 WHERE id = $2`,
			testUserID, squadID,
		); err != nil {
			t.Fatalf("archive squad %q: %v", name, err)
		}
	}
	t.Cleanup(func() {
		mustExec(t, ctx, `DELETE FROM squad WHERE id = $1`, squadID)
	})
	return squadID
}

func squadExists(t *testing.T, squadID string) bool {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM squad WHERE id = $1`, squadID,
	).Scan(&count); err != nil {
		t.Fatalf("count squad %s: %v", squadID, err)
	}
	return count == 1
}

func agentExists(t *testing.T, agentID string) bool {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent WHERE id = $1`, agentID,
	).Scan(&count); err != nil {
		t.Fatalf("count agent %s: %v", agentID, err)
	}
	return count == 1
}

func runtimeExists(t *testing.T, runtimeID string) bool {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_runtime WHERE id = $1`, runtimeID,
	).Scan(&count); err != nil {
		t.Fatalf("count runtime %s: %v", runtimeID, err)
	}
	return count == 1
}

func assertArchivedLeaderBlocksRuntimeDelete(t *testing.T, runtimeID string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodDelete, "/api/runtimes/"+runtimeID, nil), "runtimeId", runtimeID)
	testHandler.DeleteAgentRuntime(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("DeleteAgentRuntime: expected 409 archived-leader squad guard, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "active squads led by archived agents") {
		t.Fatalf("DeleteAgentRuntime: expected actionable archived-leader squad message, got body %s", w.Body.String())
	}
}

// TestDeleteSquadsByArchivedAgentsOnRuntime_Query proves exact Runtime scoping.
func TestDeleteSquadsByArchivedAgentsOnRuntime_Query(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	runtimeA := seedIsolatedRuntime(t, "Squad Cleanup Query Runtime A")
	runtimeB := seedIsolatedRuntime(t, "Squad Cleanup Query Runtime B")

	archivedOnA := seedAgentOnRuntime(t, runtimeA, "ArchivedOnA", true)
	activeOnA := seedAgentOnRuntime(t, runtimeA, "ActiveOnA", false)
	archivedOnB := seedAgentOnRuntime(t, runtimeB, "ArchivedOnB", true)

	activeSquadOnA := seedSquad(t, archivedOnA, "Active Squad On Runtime A", false)
	archivedSquadOnA := seedSquad(t, archivedOnA, "Archived Squad On Runtime A", true)
	keptActiveLeader := seedSquad(t, activeOnA, "Squad With Active Leader", false)
	keptDifferentRuntime := seedSquad(t, archivedOnB, "Squad On Runtime B", false)

	if err := testHandler.Queries.DeleteSquadsByArchivedAgentsOnRuntime(
		ctx, util.MustParseUUID(runtimeA),
	); err != nil {
		t.Fatalf("DeleteSquadsByArchivedAgentsOnRuntime: %v", err)
	}

	if !squadExists(t, activeSquadOnA) {
		t.Errorf("active squad with archived leader on target runtime must NOT be deleted")
	}
	if squadExists(t, archivedSquadOnA) {
		t.Errorf("archived squad with archived leader on target runtime should be deleted (this is the bug case)")
	}
	if !squadExists(t, keptActiveLeader) {
		t.Errorf("squad with non-archived leader must NOT be deleted")
	}
	if !squadExists(t, keptDifferentRuntime) {
		t.Errorf("squad whose leader is on a different runtime must NOT be deleted")
	}

	// Repeating the cleanup and targeting an empty Runtime are both no-ops.
	if err := testHandler.Queries.DeleteSquadsByArchivedAgentsOnRuntime(
		ctx, util.MustParseUUID(runtimeA),
	); err != nil {
		t.Fatalf("re-running DeleteSquadsByArchivedAgentsOnRuntime should be a no-op, got: %v", err)
	}

	if err := testHandler.Queries.DeleteSquadsByArchivedAgentsOnRuntime(
		ctx, util.MustParseUUID(testRuntimeID),
	); err != nil {
		t.Fatalf("no-archived-agents runtime: expected no-op, got: %v", err)
	}
}

// TestDeleteAgentRuntime_RemovesArchivedSquadsLedByArchivedAgents covers the
// complete restrictive-FK teardown.
func TestDeleteAgentRuntime_RemovesArchivedSquadsLedByArchivedAgents(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := seedIsolatedRuntime(t, "Runtime With Archived Squad Leader")
	archivedLeader := seedAgentOnRuntime(t, runtimeID, "Archived Squad Leader Agent", true)
	archivedSquad := seedSquad(t, archivedLeader, "Archived Squad For Runtime Delete", true)

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/runtimes/"+runtimeID, nil)
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.DeleteAgentRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DeleteAgentRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if squadExists(t, archivedSquad) {
		t.Errorf("squad led by archived agent on the runtime should have been deleted")
	}
	if agentExists(t, archivedLeader) {
		t.Errorf("archived agent on the runtime should have been deleted")
	}
	if runtimeExists(t, runtimeID) {
		t.Errorf("runtime should have been deleted")
	}
}

// Active Squads led by an archived Agent block Runtime deletion.
func TestDeleteAgentRuntime_ActiveSquadWithArchivedLeaderReturnsConflict(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := seedIsolatedRuntime(t, "Runtime With Active Squad And Archived Leader")
	archivedLeader := seedAgentOnRuntime(t, runtimeID, "Archived Leader Blocking Runtime Delete", true)
	activeSquad := seedSquad(t, archivedLeader, "Active Squad Blocking Runtime Delete", false)

	assertArchivedLeaderBlocksRuntimeDelete(t, runtimeID)

	if !squadExists(t, activeSquad) {
		t.Errorf("active squad must NOT have been deleted by a refused runtime delete")
	}
	if !agentExists(t, archivedLeader) {
		t.Errorf("archived leader must NOT have been deleted by a refused runtime delete")
	}
	if !runtimeExists(t, runtimeID) {
		t.Errorf("runtime must NOT have been deleted by a refused delete")
	}
}

// Mixed archived and active Squads must fail before either is deleted.
func TestDeleteAgentRuntime_ArchivedAndActiveSquadsReturnConflictWithoutDeletes(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := seedIsolatedRuntime(t, "Runtime With Archived And Active Squads")
	archivedLeader := seedAgentOnRuntime(t, runtimeID, "Archived Leader With Mixed Squads", true)
	archivedSquad := seedSquad(t, archivedLeader, "Archived Squad On Refused Delete", true)
	activeSquad := seedSquad(t, archivedLeader, "Active Squad On Refused Delete", false)

	assertArchivedLeaderBlocksRuntimeDelete(t, runtimeID)

	if !squadExists(t, archivedSquad) {
		t.Errorf("archived squad must NOT have been deleted by a refused runtime delete")
	}
	if !squadExists(t, activeSquad) {
		t.Errorf("active squad must NOT have been deleted by a refused runtime delete")
	}
	if !agentExists(t, archivedLeader) {
		t.Errorf("archived leader must NOT have been deleted by a refused runtime delete")
	}
	if !runtimeExists(t, runtimeID) {
		t.Errorf("runtime must NOT have been deleted by a refused delete")
	}
}

// A Runtime with no Squad references follows the same deletion path.
func TestDeleteAgentRuntime_NoSquadsRegression(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := seedIsolatedRuntime(t, "Runtime With No Squad References")
	archivedAgent := seedAgentOnRuntime(t, runtimeID, "Archived Agent No Squad", true)

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/runtimes/"+runtimeID, nil)
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.DeleteAgentRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DeleteAgentRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if agentExists(t, archivedAgent) {
		t.Errorf("archived agent should have been deleted")
	}
	if runtimeExists(t, runtimeID) {
		t.Errorf("runtime should have been deleted")
	}
}

// Active Agents remain an independent deletion guard.
func TestDeleteAgentRuntime_StillBlockedByActiveAgents(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := seedIsolatedRuntime(t, "Runtime With Active Agent")
	activeAgent := seedAgentOnRuntime(t, runtimeID, "Active Agent Blocking Delete", false)

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/runtimes/"+runtimeID, nil)
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.DeleteAgentRuntime(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("DeleteAgentRuntime: expected 409 active-agent guard, got %d: %s", w.Code, w.Body.String())
	}

	if !agentExists(t, activeAgent) {
		t.Errorf("active agent must NOT have been deleted by a refused runtime delete")
	}
	if !runtimeExists(t, runtimeID) {
		t.Errorf("runtime must NOT have been deleted by a refused delete")
	}
}
