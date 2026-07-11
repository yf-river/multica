package main

import (
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// fakeBroadcaster records every fanout call so tests can assert which scope a
// given event landed on.
type fakeBroadcaster struct {
	mu              sync.Mutex
	scopeCalls      []scopeCall
	userCalls       []userCall
	broadcastCalled int
}

type scopeCall struct {
	scopeType, scopeID string
	msg                []byte
}
type userCall struct {
	userID  string
	msg     []byte
	exclude string
}

func (f *fakeBroadcaster) BroadcastToScope(scopeType, scopeID string, message []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scopeCalls = append(f.scopeCalls, scopeCall{scopeType, scopeID, message})
}
func (f *fakeBroadcaster) BroadcastToUser(userID, excludeWorkspaceID string, message []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userCalls = append(f.userCalls, userCall{userID, message, excludeWorkspaceID})
}
func (f *fakeBroadcaster) Broadcast(message []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.broadcastCalled++
}

// TestRegisterListeners_TaskChatGoToWorkspace pins the must-fix #1 contract
// from the PR #1429 review: until the WS client supports scope-subscribe and
// reconnect-replay, high-frequency task/chat events MUST keep going through
// workspace scope. Routing them via BroadcastToScope("task"|"chat", ...)
// with no client-side subscriber would silently drop every chat / task
// message and break the live timeline + chat unread badges.
func TestRegisterListeners_TaskChatGoToWorkspace(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		taskID    string
		chatID    string
	}{
		{"task:message with TaskID", protocol.EventTaskMessage, "task-1", ""},
		{"task:progress with TaskID", protocol.EventTaskProgress, "task-2", ""},
		{"chat:message with ChatSessionID", protocol.EventChatMessage, "", "chat-1"},
		{"chat:done with ChatSessionID", protocol.EventChatDone, "", "chat-2"},
		{"chat:session_read with ChatSessionID", protocol.EventChatSessionRead, "", "chat-3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := events.New()
			fb := &fakeBroadcaster{}
			registerListeners(bus, fb)

			bus.Publish(events.Event{
				Type:          tc.eventType,
				WorkspaceID:   "ws-1",
				TaskID:        tc.taskID,
				ChatSessionID: tc.chatID,
				Payload:       map[string]any{"hello": "world"},
			})

			if len(fb.scopeCalls) != 1 {
				t.Fatalf("expected exactly 1 workspace scope call, got %d", len(fb.scopeCalls))
			}
			if fb.scopeCalls[0].scopeType != "workspace" || fb.scopeCalls[0].scopeID != "ws-1" {
				t.Fatalf("expected workspace/ws-1, got %s/%s", fb.scopeCalls[0].scopeType, fb.scopeCalls[0].scopeID)
			}
		})
	}
}

func TestRegisterListeners_MemberAddedExcludesWorkspaceFromUserCopy(t *testing.T) {
	bus := events.New()
	fb := &fakeBroadcaster{}
	registerListeners(bus, fb)

	bus.Publish(events.Event{
		Type:        protocol.EventMemberAdded,
		WorkspaceID: "ws-1",
		Payload: map[string]any{
			"member": map[string]any{"user_id": "user-1"},
		},
	})

	if len(fb.scopeCalls) != 1 || fb.scopeCalls[0].scopeType != "workspace" || fb.scopeCalls[0].scopeID != "ws-1" {
		t.Fatalf("workspace calls = %+v, want one workspace/ws-1 call", fb.scopeCalls)
	}
	if len(fb.userCalls) != 1 {
		t.Fatalf("user calls = %d, want 1", len(fb.userCalls))
	}
	if call := fb.userCalls[0]; call.userID != "user-1" || call.exclude != "ws-1" {
		t.Fatalf("user call = %+v, want user-1 excluding ws-1", call)
	}
}
