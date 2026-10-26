package handler

import (
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func runtimeInputFixture(t *testing.T, provider string) (supplementFixture, protocol.RuntimeInputClaim) {
	t.Helper()
	f := newSupplementFixture(t, provider, "running", false)
	dbfx.Exec(t, `UPDATE agent_task_queue SET dispatched_at=now() WHERE id=$1`, f.taskID)
	dbfx.Cleanup(t, `DELETE FROM task_runtime_input WHERE task_id=$1`, f.taskID)
	dbfx.Cleanup(t, `DELETE FROM task_runtime_capability WHERE task_id=$1`, f.taskID)
	task, err := testHandler.Queries.GetAgentTask(t.Context(), parseUUID(f.taskID))
	if err != nil {
		t.Fatal(err)
	}
	claim := protocol.RuntimeInputClaim{RuntimeID: f.runtimeID, DispatchedAt: task.DispatchedAt.Time.UTC().Format(time.RFC3339Nano)}
	return f, claim
}

func registerRuntimeInput(t *testing.T, f supplementFixture, claim protocol.RuntimeInputClaim, capabilities []string, status int) {
	t.Helper()
	testutil.Call(t, testHandler.RegisterTaskRuntimeCapability, interactionRequest(http.MethodPost, f.taskID, "", struct {
		protocol.RuntimeInputClaim
		Capabilities []string `json:"capabilities"`
	}{claim, capabilities})).Want(status)
}

func createRuntimeInput(t *testing.T, taskID, id, kind, content string, status int) protocol.TaskRuntimeInput {
	t.Helper()
	var row protocol.TaskRuntimeInput
	w := testutil.Call(t, testHandler.CreateTaskRuntimeInput, interactionRequest(http.MethodPost, taskID, "", map[string]string{"id": id, "kind": kind, "content": content})).Want(status)
	if status < 300 {
		w.JSON(&row)
	}
	return row
}

func claimRuntimeInput(t *testing.T, taskID string, claim protocol.RuntimeInputClaim, ready []string, status int) protocol.TaskRuntimeInput {
	t.Helper()
	var row protocol.TaskRuntimeInput
	w := testutil.Call(t, testHandler.ClaimTaskRuntimeInput, interactionRequest(http.MethodPost, taskID, "", struct {
		protocol.RuntimeInputClaim
		ReadyKinds []string `json:"ready_kinds"`
	}{claim, ready})).Want(status)
	if status == 200 {
		w.JSON(&row)
	}
	return row
}

func ackRuntimeInput(t *testing.T, taskID, inputID string, ack protocol.RuntimeInputAck, status int) {
	t.Helper()
	req := testutil.WithURLParams(interactionRequest(http.MethodPost, taskID, "", ack), "taskId", taskID, "inputId", inputID)
	testutil.Call(t, testHandler.AckTaskRuntimeInput, req).Want(status)
}

func TestTaskRuntimeInputNegotiationAndGeneration(t *testing.T) {
	for _, provider := range []string{"codex", "codebuddy"} {
		t.Run(provider, func(t *testing.T) {
			f, claim := runtimeInputFixture(t, provider)
			registerRuntimeInput(t, f, claim, []string{"command", "text"}, 200)
			registerRuntimeInput(t, f, claim, []string{"text", "command"}, 200)
			registerRuntimeInput(t, f, claim, []string{"command"}, 409)
			stale := claim
			stale.DispatchedAt = time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
			registerRuntimeInput(t, f, stale, []string{"command", "text"}, 409)
			stale = claim
			stale.RuntimeID = uuid.NewString()
			registerRuntimeInput(t, f, stale, []string{"command", "text"}, 409)
			registerRuntimeInput(t, f, claim, []string{"command", "command"}, 400)
			createRuntimeInput(t, f.taskID, uuid.NewString(), "command", "/goal pause", 201)
			claimRuntimeInput(t, f.taskID, stale, []string{"command"}, 204)
			if n := dbfx.Count(t, `SELECT count(*) FROM task_runtime_input WHERE task_id=$1 AND status='pending'`, f.taskID); n != 1 {
				t.Fatal("stale process consumed a pending input")
			}
		})
	}
	for _, provider := range []string{"claude", "codex"} {
		f, claim := runtimeInputFixture(t, provider)
		if provider == "codex" {
			dbfx.Exec(t, `UPDATE agent SET system_key='quality_supervision' WHERE id=$1`, f.agentID)
		}
		registerRuntimeInput(t, f, claim, []string{"command"}, 409)
	}
}

func TestTaskRuntimeInputDurableIdentityPermissionsAndReceipt(t *testing.T) {
	f, claim := runtimeInputFixture(t, "codex")
	id := uuid.NewString()
	createRuntimeInput(t, f.taskID, id, "command", "/goal pause", 409)
	registerRuntimeInput(t, f, claim, []string{"command"}, 200)
	first := createRuntimeInput(t, f.taskID, id, "command", "/goal pause", 201)
	repeated := createRuntimeInput(t, f.taskID, id, "command", "/goal pause", 200)
	if first.CreatedAt != repeated.CreatedAt {
		t.Fatal("idempotent submit replaced the original input")
	}
	createRuntimeInput(t, f.taskID, id, "command", "/goal clear", 409)
	createRuntimeInput(t, f.taskID, uuid.NewString(), "text", "unsupported text", 409)
	createRuntimeInput(t, f.taskID, uuid.NewString(), "command", "/delete anything", 400)
	req := interactionRequest(http.MethodPost, f.taskID, "", map[string]string{"id": uuid.NewString(), "kind": "command", "content": "/goal clear"})
	req.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.CreateTaskRuntimeInput, req).Want(403)
	req = interactionRequest(http.MethodGet, f.taskID, "", nil)
	req = req.WithContext(middleware.SetMemberContext(req.Context(), uuid.NewString(), db.Member{}))
	testutil.Call(t, testHandler.ListTaskRuntimeInputs, req).Want(404)
	ack := protocol.RuntimeInputAck{RuntimeInputClaim: claim, Status: "delivered", Output: "已暂停"}
	ackRuntimeInput(t, f.taskID, id, ack, 409)
	if got := claimRuntimeInput(t, f.taskID, claim, []string{"command"}, 200); got.ID != id || got.Status != "delivering" {
		t.Fatal("claim did not preserve the exact durable identity")
	}
	claimRuntimeInput(t, f.taskID, claim, []string{"command"}, 204)
	ackRuntimeInput(t, f.taskID, id, ack, 200)
	ackRuntimeInput(t, f.taskID, id, ack, 200)
	ack.Output = "changed"
	ackRuntimeInput(t, f.taskID, id, ack, 409)
	dbfx.Exec(t, `UPDATE agent SET owner_id=NULL WHERE id=$1`, f.agentID)
	createRuntimeInput(t, f.taskID, uuid.NewString(), "command", "/goal clear", 403)
}

func TestTaskRuntimeInputReadinessAndTerminalSettlement(t *testing.T) {
	f, claim := runtimeInputFixture(t, "codebuddy")
	registerRuntimeInput(t, f, claim, []string{"text", "command"}, 200)
	textID, commandID := uuid.NewString(), uuid.NewString()
	createRuntimeInput(t, f.taskID, textID, "text", "keep the original objective", 201)
	createRuntimeInput(t, f.taskID, commandID, "command", "/goal pause", 201)
	if got := claimRuntimeInput(t, f.taskID, claim, []string{"command"}, 200); got.ID != commandID {
		t.Fatal("not-ready text prevented a ready pause command")
	}
	if w := failTaskViaHandler(t, f.taskID); w.Code != 200 {
		t.Fatalf("task failure: %d: %s", w.Code, w.Body.String())
	}
	for id, reason := range map[string]string{textID: "turn_ended", commandID: "delivery_unknown"} {
		row, err := testHandler.Queries.GetTaskRuntimeInput(t.Context(), db.GetTaskRuntimeInputParams{TaskID: parseUUID(f.taskID), ID: parseUUID(id)})
		if err != nil || row.Status != "failed" || row.FailureReason != reason {
			t.Fatalf("terminal settlement: status=%s reason=%s err=%v", row.Status, row.FailureReason, err)
		}
	}
	claimRuntimeInput(t, f.taskID, claim, []string{"command", "text"}, 204)
	createRuntimeInput(t, f.taskID, uuid.NewString(), "command", "/goal resume", 409)
	ackRuntimeInput(t, f.taskID, commandID, protocol.RuntimeInputAck{RuntimeInputClaim: claim, Status: "delivered", Output: "原生暂停已确认"}, 200)
	ackRuntimeInput(t, f.taskID, textID, protocol.RuntimeInputAck{RuntimeInputClaim: claim, Status: "delivered"}, 409)
}

func TestTaskRuntimeInputLostReceiptBecomesUncertainWithoutReplay(t *testing.T) {
	f, claim := runtimeInputFixture(t, "codex")
	registerRuntimeInput(t, f, claim, []string{"command"}, 200)
	id := uuid.NewString()
	createRuntimeInput(t, f.taskID, id, "command", "/goal pause", 201)
	claimRuntimeInput(t, f.taskID, claim, []string{"command"}, 200)
	dbfx.Exec(t, `UPDATE task_runtime_input SET claimed_at=now()-interval '31 seconds' WHERE task_id=$1`, f.taskID)
	var response struct {
		Inputs []protocol.TaskRuntimeInput `json:"inputs"`
	}
	testutil.Call(t, testHandler.ListTaskRuntimeInputs, interactionRequest(http.MethodGet, f.taskID, "", nil)).Want(200).JSON(&response)
	if len(response.Inputs) != 1 || response.Inputs[0].FailureReason != "delivery_unknown" {
		t.Fatal("lost claim receipt remained indefinitely in progress")
	}
	claimRuntimeInput(t, f.taskID, claim, []string{"command"}, 204)
	ackRuntimeInput(t, f.taskID, id, protocol.RuntimeInputAck{RuntimeInputClaim: claim, Status: "delivered", Output: "paused"}, 200)
}

func TestTaskRuntimeInputConcurrentDuplicatesAndCompletePagination(t *testing.T) {
	f, claim := runtimeInputFixture(t, "codex")
	registerRuntimeInput(t, f, claim, []string{"text"}, 200)
	id := uuid.NewString()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := interactionRequest(http.MethodPost, f.taskID, "", map[string]string{"id": id, "kind": "text", "content": "same logical input"})
			w := testutil.Call(t, testHandler.CreateTaskRuntimeInput, req)
			if w.Code != 200 && w.Code != 201 {
				t.Errorf("concurrent submit: %d", w.Code)
			}
		}()
	}
	wg.Wait()
	if n := dbfx.Count(t, `SELECT count(*) FROM task_runtime_input WHERE task_id=$1`, f.taskID); n != 1 {
		t.Fatalf("duplicate created %d rows", n)
	}
	dbfx.Exec(t, `INSERT INTO task_runtime_input (task_id,id,workspace_id,issue_id,author_id,kind,content,created_at)
 SELECT $1,gen_random_uuid(),$2,$3,$4,'text','history '||n,now() FROM generate_series(1,207) n`, f.taskID, testWorkspaceID, f.issueID, testUserID)
	seen, cursor := map[string]bool{}, ""
	for range 4 {
		req := interactionRequest(http.MethodGet, f.taskID, "", nil)
		req.URL.RawQuery = "cursor=" + url.QueryEscape(cursor)
		var response struct {
			Inputs     []protocol.TaskRuntimeInput `json:"inputs"`
			NextCursor string                      `json:"next_cursor"`
		}
		testutil.Call(t, testHandler.ListTaskRuntimeInputs, req).Want(200).JSON(&response)
		for _, row := range response.Inputs {
			if seen[row.ID] {
				t.Fatal("duplicate at page boundary")
			}
			seen[row.ID] = true
		}
		cursor = response.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 208 || cursor != "" {
		t.Fatalf("pagination lost history: seen=%d unfinished=%t", len(seen), cursor != "")
	}
	req := interactionRequest(http.MethodGet, f.taskID, "", nil)
	req.URL.RawQuery = "cursor=malformed"
	testutil.Call(t, testHandler.ListTaskRuntimeInputs, req).Want(400)
}

func TestTaskRuntimeInputPrivateChatAndExplicitDelete(t *testing.T) {
	f, claim := runtimeInputFixture(t, "codebuddy")
	chatID := dbfx.ChatSession(t, f.agentID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET issue_id=NULL,chat_session_id=$2 WHERE id=$1`, f.taskID, chatID)
	registerRuntimeInput(t, f, claim, []string{"text", "command"}, 200)
	id := uuid.NewString()
	createRuntimeInput(t, f.taskID, id, "text", "private guidance", 201)
	otherID := dbfx.User(t, "Runtime input outsider", uuid.NewString()+"@example.com")
	req := interactionRequest(http.MethodGet, f.taskID, "", nil)
	req.Header.Set("X-User-ID", otherID)
	testutil.Call(t, testHandler.ListTaskRuntimeInputs, req).Want(403)
	req = interactionRequest(http.MethodDelete, f.taskID, "", nil)
	req = testutil.WithURLParams(req, "sessionId", chatID)
	testutil.Call(t, testHandler.DeleteChatSession, req).Want(204)
	for _, table := range []string{"task_runtime_input", "task_runtime_capability"} {
		if n := dbfx.Count(t, `SELECT count(*) FROM `+table+` WHERE task_id=$1`, f.taskID); n != 0 {
			t.Fatalf("chat deletion left %s rows", table)
		}
	}
}

func TestTaskRuntimeInputCannotExposeDeletedPrivateChatAfterRollback(t *testing.T) {
	f, claim := runtimeInputFixture(t, "codex")
	chatID := dbfx.ChatSession(t, f.agentID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET issue_id=NULL,chat_session_id=$2 WHERE id=$1`, f.taskID, chatID)
	registerRuntimeInput(t, f, claim, []string{"text"}, 200)
	createRuntimeInput(t, f.taskID, uuid.NewString(), "text", "private guidance", 201)
	// 旧程序删除会话时不认识新表。即使关系被置空，私有输入也不能变成普通工作区历史。
	dbfx.Exec(t, `UPDATE agent_task_queue SET chat_session_id=NULL WHERE id=$1`, f.taskID)
	testutil.Call(t, testHandler.ListTaskRuntimeInputs, interactionRequest(http.MethodGet, f.taskID, "", nil)).Want(404)
	rows, err := testHandler.Queries.ListTaskRuntimeInputs(t.Context(), db.ListTaskRuntimeInputsParams{TaskID: parseUUID(f.taskID), BeforeCreatedAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, BeforeID: parseUUID("ffffffff-ffff-ffff-ffff-ffffffffffff"), PageLimit: 100})
	if err != nil || len(rows) != 0 {
		t.Fatal("detached private inputs became workspace-visible")
	}
	claimRuntimeInput(t, f.taskID, claim, []string{"text"}, 404)
}
