package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestMikaChatReuseIsMemberScopedAndPermissionChecked(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	workspace := dbfx.Workspace(t, "Mika shortcut fixture", fmt.Sprintf("mika-shortcut-%d", time.Now().UnixNano()))
	fx := testutil.New(testPool, workspace, testUserID)
	fx.Member(t, workspace, testUserID, "owner")
	other := fx.User(t, "Shortcut Member", fmt.Sprintf("mika-shortcut-%d@test.local", time.Now().UnixNano()))
	fx.Member(t, workspace, other, "member")
	agent := fx.Agent(t, "Renamed Mika", "", testutil.Cols{"system_key": "mika", "permission_mode": "public_to", "visibility": "workspace"})
	fx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": agent, "target_type": "workspace", "target_id": workspace, "created_by": testUserID})
	fx.Cleanup(t, "DELETE FROM chat_session WHERE agent_id = $1", agent)
	call := func(user, agentID string, reuse bool, status int) ChatSessionResponse {
		t.Helper()
		member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{UserID: parseUUID(user), WorkspaceID: parseUUID(workspace)})
		if err != nil {
			t.Fatal(err)
		}
		r := newRequestAs(user, "POST", "/api/chat/sessions", map[string]any{"agent_id": agentID, "title": "display title", "reuse_existing": reuse})
		r = r.WithContext(middleware.SetMemberContext(r.Context(), workspace, member))
		var result ChatSessionResponse
		testutil.Call(t, testHandler.CreateChatSession, r).Want(status).JSON(&result)
		return result
	}
	first := call(testUserID, agent, true, http.StatusOK)
	if first.ID == "" || first.CreatorID != testUserID {
		t.Fatal("Mika chat did not use the current member")
	}
	for i := 0; i < 3; i++ {
		if call(testUserID, agent, true, http.StatusOK).ID != first.ID {
			t.Fatal("repeat shortcut clicks created a new conversation")
		}
	}
	second := call(other, agent, true, http.StatusOK)
	if second.ID == first.ID || second.CreatorID != other {
		t.Fatal("Mika chat leaked another member's conversation")
	}
	fx.Exec(t, "UPDATE agent SET permission_mode = 'private', visibility = 'private' WHERE id = $1", agent)
	call(other, agent, true, http.StatusForbidden)
	fx.Exec(t, "UPDATE agent SET archived_at = now() WHERE id = $1", agent)
	call(testUserID, agent, true, http.StatusBadRequest)
	normal := fx.Agent(t, "Mika", "")
	call(testUserID, normal, true, http.StatusBadRequest)
}
