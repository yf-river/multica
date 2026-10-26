package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTaskContextCursor(t *testing.T) {
	id := "00000000-0000-0000-0000-000000000123"
	for _, test := range []struct {
		name, cursor, limit string
		wantSeq             int32
		wantError           bool
	}{
		{"first page", "", "", -1, false},
		{"resume", base64.RawURLEncoding.EncodeToString([]byte("5001:" + id)), "1000", 5001, false},
		{"bad base64", "!", "", 0, true},
		{"bad uuid", base64.RawURLEncoding.EncodeToString([]byte("2:not-a-uuid")), "", 0, true},
		{"negative seq", base64.RawURLEncoding.EncodeToString([]byte("-1:" + id)), "", 0, true},
		{"overflow", base64.RawURLEncoding.EncodeToString([]byte("2147483648:" + id)), "", 0, true},
		{"bad limit", "", "-1", 0, true},
		{"large limit", "", "1001", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			seq, gotID, limit, err := parseTaskContextPage(test.cursor, test.limit)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v", err)
			}
			if err != nil {
				return
			}
			if seq != test.wantSeq || !gotID.Valid || limit < 1 || limit > 1000 {
				t.Fatalf("invalid parsed cursor: seq=%d id=%v limit=%d", seq, gotID, limit)
			}
			if test.cursor != "" && uuidToString(gotID) != id {
				t.Fatalf("cursor UUID changed: %v", gotID)
			}
		})
	}
}

func TestTaskContextPagesBeyondFiveThousand(t *testing.T) {
	taskID := seedBatchTask(t, "runtime-pages")
	var issueID string
	dbfx.QueryRow(t, `SELECT issue_id FROM agent_task_queue WHERE id=$1`, taskID).Scan(&issueID)
	messages := make([]any, 0, 5210)
	for i := 0; i < 5210; i++ {
		content := fmt.Sprintf(`{"kind":"context","item_id":"%d"}`, i)
		if i < 8 {
			content = "malformed"
		}
		// 相同 seq 的历史行由 UUID 排序继续翻页，不允许丢失或重复。
		messages = append(messages, map[string]any{"seq": i / 2, "type": "runtime", "content": content})
	}
	testutil.Call(t, testHandler.ReportTaskMessages, batchMessagesRequest(t, taskID, messages)).Want(http.StatusOK)
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 30; pages++ {
		req := testutil.JSONRequest(http.MethodGet, "/api/issues/"+issueID+"/tasks/"+taskID+"/context?limit=300&after="+url.QueryEscape(cursor), nil)
		req = testutil.WithURLParams(req, "id", issueID, "taskId", taskID)
		req.Header.Set("X-User-ID", testUserID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		var page struct {
			Events []json.RawMessage `json:"events"`
			Next   string            `json:"next_cursor"`
			More   bool              `json:"has_more"`
		}
		testutil.Call(t, testHandler.GetTaskContext, req).Want(http.StatusOK).JSON(&page)
		for _, raw := range page.Events {
			var event struct {
				ID string `json:"item_id"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if seen[event.ID] {
				t.Fatalf("duplicate event %s", event.ID)
			}
			seen[event.ID] = true
		}
		if !page.More {
			if len(seen) != 5202 {
				t.Fatalf("history truncated: %d events", len(seen))
			}
			return
		}
		if page.Next == "" || cursor == page.Next {
			t.Fatal("pagination did not advance")
		}
		cursor = page.Next
	}
	t.Fatal("pagination did not terminate")
}

func TestChatTaskContextUsesPrivateChatVisibility(t *testing.T) {
	runtimeID := dbfx.Runtime(t, "chat telemetry", testutil.Cols{"provider": "codex"})
	agentID := dbfx.Agent(t, "chat telemetry "+uuid.NewString(), runtimeID)
	chatID := dbfx.ChatSession(t, agentID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "chat_session_id": chatID, "status": "running"})
	messages := []any{map[string]any{"seq": 0, "type": "runtime", "content": `{"kind":"context","provider":"codex","pressure_tokens":12}`}, map[string]any{"seq": 1, "type": "runtime", "content": `{"kind":"compaction","phase":"end"}`}}
	testutil.Call(t, testHandler.ReportTaskMessages, batchMessagesRequest(t, taskID, messages)).Want(200)
	req := interactionRequest(http.MethodGet, taskID, "", nil)
	req.URL.RawQuery = "limit=1"
	var page struct {
		Events []json.RawMessage `json:"events"`
		More   bool              `json:"has_more"`
		Next   string            `json:"next_cursor"`
	}
	testutil.Call(t, testHandler.GetTaskContextByUser, req).Want(200).JSON(&page)
	if len(page.Events) != 1 || !page.More || page.Next == "" {
		t.Fatalf("chat page incomplete: %+v", page)
	}
	req = interactionRequest(http.MethodGet, taskID, "", nil)
	req.Header.Set("X-User-ID", uuid.NewString())
	testutil.Call(t, testHandler.GetTaskContextByUser, req).Want(403)
	req = interactionRequest(http.MethodGet, taskID, "", nil)
	req = req.WithContext(middleware.SetMemberContext(req.Context(), uuid.NewString(), db.Member{}))
	testutil.Call(t, testHandler.GetTaskContextByUser, req).Want(404)
}
