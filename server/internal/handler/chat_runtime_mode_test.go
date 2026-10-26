package handler

import (
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
)

func TestChatPlanModeIndependentOfGoal(t *testing.T) {
	history := []db.ChatMessage{{Role: "user", Content: "/plan"}, {Role: "assistant", Content: "/plan off"}, {Role: "user", Content: "/goal complete the task"}}
	mode := chatPlanModeSelection(history)
	if mode == nil || !*mode {
		t.Fatal("goal or assistant text overwrote selected plan mode")
	}
	if command := chatRuntimeCommand(history[len(history)-1:]); command != "/goal complete the task" {
		t.Fatalf("goal command lost: %q", command)
	}
	history = append(history, db.ChatMessage{Role: "user", Content: "/plan off"})
	mode = chatPlanModeSelection(history)
	if mode == nil || *mode {
		t.Fatal("plan off not persisted")
	}
	if chatPlanModeSelection([]db.ChatMessage{{Role: "user", Content: "ordinary message"}}) != nil {
		t.Fatal("invented an explicit mode")
	}
}
