package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDSHSuspendPersistsInteractionAndRevokesPermissions(t *testing.T) {
	runtimeID := dbfx.Runtime(t, "DSH suspended runtime", testutil.Cols{"provider": "dsh"})
	agentID := dbfx.Agent(t, "DSH suspended "+uuid.NewString(), runtimeID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": dbfx.Issue(t, "DSH plan"), "status": "running"})
	dbfx.Cleanup(t, `DELETE FROM task_interaction WHERE task_id=$1`, taskID)
	dbfx.Cleanup(t, `DELETE FROM task_token WHERE task_id=$1`, taskID)
	tokenHash := "suspended-" + uuid.NewString()
	dbfx.Exec(t, `INSERT INTO task_token (id, token_hash, task_id, agent_id, workspace_id, user_id, expires_at) VALUES ($1,$2,$3,$4,$5,$6,now()+interval '1 hour')`, uuid.NewString(), tokenHash, taskID, agentID, testWorkspaceID, testUserID)
	pending := protocol.SuspendedInteraction{CallID: "native-plan-call", InteractionPrompt: protocol.InteractionPrompt{Kind: "plan", Title: "Plan", PlanMarkdown: "# Plan\n\nWait for approval."}}
	request := map[string]any{"session_id": "native-plan-session", "work_dir": "/isolated/plan", "output": "Plan", "interaction": pending}
	testutil.Call(t, testHandler.SuspendTask, interactionRequest(http.MethodPost, taskID, "", request)).Want(200)
	testutil.Call(t, testHandler.SuspendTask, interactionRequest(http.MethodPost, taskID, "", request)).Want(200)
	task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(taskID))
	if err != nil || task.Status != "waiting_plan" || task.SessionID.String != "native-plan-session" || task.WorkDir.String != "/isolated/plan" {
		t.Fatalf("suspension lost native state: %+v %v", task, err)
	}
	if _, err := testHandler.Queries.GetTaskTokenByHash(context.Background(), tokenHash); err == nil {
		t.Fatal("suspended process retained task permissions")
	}
	rows, err := testHandler.Queries.ListTaskInteractions(context.Background(), parseUUID(taskID))
	if err != nil || len(rows) != 1 {
		t.Fatalf("suspension replay duplicated interaction: %+v %v", rows, err)
	}
	response := interactionResponse(rows[0], task.Status)
	if response.Status != "pending" || response.DeliveryMode != "suspended" {
		t.Fatalf("released process expired its durable question: %+v", response)
	}
	var stored protocol.SuspendedInteraction
	if json.Unmarshal(rows[0].Request, &stored) != nil || stored.PlanMarkdown != pending.PlanMarkdown || stored.CallID != pending.CallID {
		t.Fatal("native plan was changed")
	}
	pending.PlanMarkdown += " changed"
	if _, err := testHandler.TaskService.SuspendTaskForInteraction(context.Background(), task.ID, pending, []byte(`{"output":"Plan"}`), task.SessionID.String, task.WorkDir.String); err != service.ErrInteractionConflict {
		t.Fatalf("changed replay accepted: %v", err)
	}
	dbfx.Exec(t, `UPDATE task_interaction SET expires_at=now()-interval '1 second' WHERE task_id=$1`, taskID)
	rows, _ = testHandler.Queries.ListTaskInteractions(context.Background(), parseUUID(taskID))
	if interactionResponse(rows[0], task.Status).Status != "expired" {
		t.Fatal("expired plan remains actionable")
	}
}

func TestDSHPlanDecisionCreatesOneContinuation(t *testing.T) {
	for _, scope := range []string{"chat", "issue"} {
		for _, decision := range []string{"approved", "keep_planning", "cancelled"} {
			t.Run(scope+"/"+decision, func(t *testing.T) {
				runtimeID := dbfx.Runtime(t, "DSH continuation", testutil.Cols{"provider": "dsh"})
				agentID := dbfx.Agent(t, "DSH continuation "+uuid.NewString(), runtimeID)
				cols := testutil.Cols{"runtime_id": runtimeID, "status": "running"}
				if scope == "chat" {
					cols["chat_session_id"] = dbfx.ChatSession(t, agentID)
				} else {
					cols["issue_id"] = dbfx.Issue(t, "Plan continuation")
				}
				taskID := dbfx.Task(t, agentID, cols)
				dbfx.Cleanup(t, `DELETE FROM task_interaction WHERE task_id=$1`, taskID)
				dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE agent_id=$1`, agentID)
				pending := protocol.SuspendedInteraction{CallID: "plan-call", InteractionPrompt: protocol.InteractionPrompt{Kind: "plan", Title: "Plan", PlanMarkdown: "# Plan\n\nDo work after approval."}}
				testutil.Call(t, testHandler.SuspendTask, interactionRequest(http.MethodPost, taskID, "", map[string]any{"session_id": "session-" + taskID, "work_dir": "/isolated/" + taskID, "output": "Plan", "interaction": pending})).Want(200)
				rows, err := testHandler.Queries.ListTaskInteractions(context.Background(), parseUUID(taskID))
				if err != nil || len(rows) != 1 {
					t.Fatalf("missing pending plan: %v", err)
				}
				id := uuidToString(rows[0].ID)
				answer := protocol.InteractionAnswer{Decision: decision}
				if decision == "keep_planning" {
					answer.Feedback = "Add a rollback step."
				}
				var first, again protocol.TaskInteraction
				testutil.Call(t, testHandler.RespondTaskInteraction, interactionRequest(http.MethodPost, taskID, id, answer)).Want(200).JSON(&first)
				testutil.Call(t, testHandler.RespondTaskInteraction, interactionRequest(http.MethodPost, taskID, id, answer)).Want(200).JSON(&again)
				testutil.Call(t, testHandler.SuspendTask, interactionRequest(http.MethodPost, taskID, "", map[string]any{"session_id": "session-" + taskID, "work_dir": "/isolated/" + taskID, "output": "Plan", "interaction": pending})).Want(200)
				if first.Status != "responded" || first.FollowupTaskID != again.FollowupTaskID {
					t.Fatalf("decision was not idempotent: %+v %+v", first, again)
				}
				parent, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(taskID))
				if err != nil {
					t.Fatal(err)
				}
				if decision == "cancelled" {
					if first.FollowupTaskID != "" || parent.Status != "cancelled" {
						t.Fatalf("cancelled plan queued work: %+v %+v", first, parent)
					}
				} else {
					if first.FollowupTaskID == "" || parent.Status != "completed" {
						t.Fatalf("decision did not enqueue continuation: %+v %+v", first, parent)
					}
					next, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(first.FollowupTaskID))
					if err != nil || next.Status != "queued" || next.AgentID != parent.AgentID || next.RuntimeID != parent.RuntimeID || next.OriginatorUserID != parseUUID(testUserID) {
						t.Fatalf("continuation lost identity: %+v %v", next, err)
					}
					runtime, err := testHandler.Queries.GetAgentRuntime(context.Background(), next.RuntimeID)
					if err != nil {
						t.Fatal(err)
					}
					claimRequest := newDaemonTokenRequest("POST", "/claim", nil, testWorkspaceID, "dsh-continuation-test")
					claimRequest.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityDSHInteractionsV1)
					claimed, _, _, _, failure := testHandler.buildClaimedTaskResponse(claimRequest, &next, runtime, runtimeID, testWorkspaceID)
					if failure != nil {
						t.Fatalf("continuation claim rejected: %+v", failure)
					}
					wantCommand := "/plan off"
					if decision == "keep_planning" {
						wantCommand = "/plan"
					}
					if !claimed.InteractionContinuation || claimed.InteractionSourceTaskID != taskID || claimed.PlanDecision != decision || claimed.Command != wantCommand || claimed.PriorSessionID != "session-"+taskID || claimed.PriorWorkDir != "/isolated/"+taskID {
						t.Fatalf("claim changed native decision or continuity: %+v", claimed)
					}
					binding, err := testHandler.Queries.GetTaskInteractionFollowup(context.Background(), next.ID)
					if err != nil || binding.ID != rows[0].ID {
						t.Fatalf("continuation lost native session binding: %+v %v", binding, err)
					}
				}
				changed := protocol.InteractionAnswer{Decision: "cancelled"}
				if decision == "cancelled" {
					changed.Decision = "approved"
				}
				testutil.Call(t, testHandler.RespondTaskInteraction, interactionRequest(http.MethodPost, taskID, id, changed)).Want(409)
			})
		}
	}
}

func suspendedPlanFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	runtimeID := dbfx.Runtime(t, "DSH plan guard", testutil.Cols{"provider": "dsh"})
	agentID := dbfx.Agent(t, "DSH guard "+uuid.NewString(), runtimeID)
	issueID := dbfx.Issue(t, "DSH plan guard")
	taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID, "status": "running"})
	dbfx.Cleanup(t, `DELETE FROM task_interaction WHERE task_id=$1`, taskID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE agent_id=$1`, agentID)
	pending := protocol.SuspendedInteraction{CallID: "guard-plan", InteractionPrompt: protocol.InteractionPrompt{Kind: "plan", Title: "Guard plan", PlanMarkdown: "# Guard plan\n\nWait for approval."}}
	_, err := testHandler.TaskService.SuspendTaskForInteraction(context.Background(), parseUUID(taskID), pending, []byte(`{"output":"Guard plan"}`), "guard-session", "/isolated/guard")
	if err != nil {
		t.Fatal(err)
	}
	row, err := testHandler.Queries.GetSuspendedTaskInteraction(context.Background(), parseUUID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	return taskID, uuidToString(row.ID), agentID, issueID
}

func TestDSHPlanRejectsStaleExpiredCancelledOrReboundTask(t *testing.T) {
	for _, state := range []string{"expired", "newer-task", "cancelled", "runtime-changed"} {
		t.Run(state, func(t *testing.T) {
			taskID, id, agentID, issueID := suspendedPlanFixture(t)
			switch state {
			case "expired":
				dbfx.Exec(t, `UPDATE task_interaction SET expires_at=now()-interval '1 second' WHERE task_id=$1`, taskID)
			case "newer-task":
				source, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(taskID))
				if err != nil {
					t.Fatal(err)
				}
				dbfx.Task(t, agentID, testutil.Cols{"issue_id": issueID, "runtime_id": uuidToString(source.RuntimeID), "status": "completed"})
			case "cancelled":
				if _, err := testHandler.TaskService.CancelTaskWithResult(context.Background(), parseUUID(taskID), service.CancelTaskOptions{UserInitiated: true}); err != nil {
					t.Fatal(err)
				}
			case "runtime-changed":
				nextRuntime := dbfx.Runtime(t, "Changed DSH runtime", testutil.Cols{"provider": "dsh"})
				dbfx.Exec(t, `UPDATE agent SET runtime_id=$1 WHERE id=$2`, nextRuntime, agentID)
			}
			testutil.Call(t, testHandler.RespondTaskInteraction, interactionRequest(http.MethodPost, taskID, id, protocol.InteractionAnswer{Decision: "approved"})).Want(409)
			row, err := testHandler.Queries.GetTaskInteraction(context.Background(), db.GetTaskInteractionParams{TaskID: parseUUID(taskID), ID: parseUUID(id)})
			if err != nil || row.FollowupTaskID.Valid {
				t.Fatalf("unsafe plan queued work: %+v %v", row, err)
			}
		})
	}
}

func TestDSHConcurrentApprovalCreatesOneTask(t *testing.T) {
	taskID, id, _, _ := suspendedPlanFixture(t)
	type outcome struct {
		row db.TaskInteraction
		err error
	}
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			row, err := testHandler.TaskService.RespondSuspendedInteraction(context.Background(), parseUUID(taskID), parseUUID(id), parseUUID(testUserID), protocol.InteractionAnswer{Decision: "approved"})
			outcomes <- outcome{row, err}
		}()
	}
	first, second := <-outcomes, <-outcomes
	if first.err != nil || second.err != nil || !first.row.FollowupTaskID.Valid || first.row.FollowupTaskID != second.row.FollowupTaskID {
		t.Fatalf("concurrent approval duplicated or lost task: %+v %+v", first, second)
	}
}

func TestDSHExpiredInteractionIsDurablyCancelled(t *testing.T) {
	taskID, id, _, _ := suspendedPlanFixture(t)
	dbfx.Exec(t, `UPDATE task_interaction SET expires_at=now()-interval '1 second' WHERE task_id=$1`, taskID)
	if err := testHandler.TaskService.ExpireSuspendedInteractions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := testHandler.TaskService.ExpireSuspendedInteractions(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(taskID))
	if err != nil || task.Status != "cancelled" {
		t.Fatalf("expired task still waits: %+v %v", task, err)
	}
	row, err := testHandler.Queries.GetTaskInteraction(context.Background(), db.GetTaskInteractionParams{TaskID: parseUUID(taskID), ID: parseUUID(id)})
	if err != nil || row.Status != "expired" || row.FollowupTaskID.Valid {
		t.Fatalf("expired interaction was not settled: %+v %v", row, err)
	}
	testutil.Call(t, testHandler.RespondTaskInteraction, interactionRequest(http.MethodPost, taskID, id, protocol.InteractionAnswer{Decision: "approved"})).Want(409)
}
