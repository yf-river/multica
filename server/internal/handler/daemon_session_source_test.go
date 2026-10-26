package handler

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCodexSessionSourceScope(t *testing.T) {
	f := newSupplementFixture(t, "codex", "dispatched", false)
	runtime, err := testHandler.Queries.GetAgentRuntime(t.Context(), parseUUID(f.runtimeID))
	if err != nil {
		t.Fatal(err)
	}
	task, err := testHandler.Queries.GetAgentTask(t.Context(), parseUUID(f.taskID))
	if err != nil {
		t.Fatal(err)
	}
	older := task.CreatedAt.Time.Add(-time.Minute)
	source := dbfx.Task(t, f.agentID, testutil.Cols{
		"runtime_id": f.runtimeID, "issue_id": f.issueID, "status": "completed",
		"session_id": "owned-native-session", "created_at": older, "completed_at": older,
	})
	response := AgentTaskResponse{PriorSessionID: "owned-native-session"}
	if err := testHandler.resolveCodexSessionSource(t.Context(), task, runtime, &response); err != nil || response.PriorTaskID != source {
		t.Fatalf("selected native state source=%q want=%q error=%v", response.PriorTaskID, source, err)
	}

	otherIssue := dbfx.Issue(t, "other native state scope")
	otherAgent := dbfx.Agent(t, "other native state agent", f.runtimeID)
	otherRuntime := dbfx.Runtime(t, "other native state runtime", testutil.Cols{"provider": "codex"})
	otherChat := dbfx.ChatSession(t, f.agentID)
	for _, tc := range []struct {
		name string
		cols testutil.Cols
	}{
		{"other issue", testutil.Cols{"issue_id": otherIssue}},
		{"other runtime", testutil.Cols{"runtime_id": otherRuntime}},
		{"chat instead of issue", testutil.Cols{"chat_session_id": otherChat}},
		{"future task", testutil.Cols{"created_at": task.CreatedAt.Time.Add(time.Hour)}},
		{"active task", testutil.Cols{"status": "running"}},
		{"different session", testutil.Cols{"session_id": "another-native-session"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cols := testutil.Cols{"runtime_id": f.runtimeID, "issue_id": f.issueID, "status": "completed", "session_id": "owned-native-session", "created_at": older}
			for key, value := range tc.cols {
				cols[key] = value
			}
			id := dbfx.Task(t, f.agentID, cols)
			_, err := testHandler.Queries.GetTaskSessionSource(t.Context(), db.GetTaskSessionSourceParams{TaskID: task.ID, SessionID: response.PriorSessionID, SourceTaskID: parseUUID(id)})
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("out-of-scope source accepted: %v", err)
			}
		})
	}
	foreignSource := dbfx.Task(t, otherAgent, testutil.Cols{"runtime_id": f.runtimeID, "issue_id": f.issueID, "status": "completed", "session_id": response.PriorSessionID, "created_at": older})
	task.RerunOfTaskID = parseUUID(foreignSource)
	response.PriorTaskID = ""
	if err := testHandler.resolveCodexSessionSource(t.Context(), task, runtime, &response); err != nil || response.PriorTaskID != "" {
		t.Fatalf("rerun substituted another source: %q %v", response.PriorTaskID, err)
	}
	task.RerunOfTaskID = parseUUID(source)
	if err := testHandler.resolveCodexSessionSource(t.Context(), task, runtime, &response); err != nil || response.PriorTaskID != source {
		t.Fatalf("exact rerun source: %q %v", response.PriorTaskID, err)
	}
	response.PriorTaskID = ""
	runtime.Provider = "codebuddy"
	if err := testHandler.resolveCodexSessionSource(t.Context(), task, runtime, &response); err != nil || response.PriorTaskID != "" {
		t.Fatal("Codex source lookup affected another provider")
	}

	dbfx.Exec(t, `UPDATE agent_task_queue SET issue_id=NULL,chat_session_id=$2,channel_context_revision=2 WHERE id=$1`, f.taskID, otherChat)
	task.ChatSessionID, task.IssueID, task.RerunOfTaskID = parseUUID(otherChat), pgtype.UUID{}, pgtype.UUID{}
	task.ChannelContextRevision = pgtype.Int8{Int64: 2, Valid: true}
	for _, revision := range []int{1, 2} {
		id := dbfx.Task(t, f.agentID, testutil.Cols{"runtime_id": f.runtimeID, "chat_session_id": otherChat, "channel_context_revision": revision, "status": "completed", "session_id": "chat-native-session", "created_at": older})
		_, err := testHandler.Queries.GetTaskSessionSource(t.Context(), db.GetTaskSessionSourceParams{TaskID: task.ID, SessionID: "chat-native-session", SourceTaskID: parseUUID(id)})
		if (revision == 1 && !errors.Is(err, pgx.ErrNoRows)) || (revision == 2 && err != nil) {
			t.Fatalf("chat revision %d: %v", revision, err)
		}
	}
}

func TestCodexSessionSourceUsesLatestCompletedProducer(t *testing.T) {
	f := newSupplementFixture(t, "codex", "dispatched", false)
	dbfx.Exec(t, `UPDATE agent_task_queue SET created_at=now()-interval '5 minutes' WHERE id=$1`, f.taskID)
	old := time.Now().Add(-10 * time.Minute)
	newer := time.Now().Add(-time.Minute)
	for _, created := range []time.Time{old, newer} {
		id := dbfx.Task(t, f.agentID, testutil.Cols{"runtime_id": f.runtimeID, "issue_id": f.issueID, "status": "completed", "created_at": created, "completed_at": created, "session_id": "continued-native-session"})
		if created.Equal(newer) {
			source, err := testHandler.Queries.GetTaskSessionSource(t.Context(), db.GetTaskSessionSourceParams{TaskID: parseUUID(f.taskID), SessionID: "continued-native-session"})
			if err != nil || uuidToString(source) != id {
				t.Fatalf("selected stale native state instead of the latest completed producer: %s %v", uuidToString(source), err)
			}
		}
	}
}
