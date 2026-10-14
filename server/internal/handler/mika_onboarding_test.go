package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestNormalizeMessageKindPreservesOnboardingKickoff(t *testing.T) {
	if got := normalizeMessageKind(protocol.ChatMessageKindOnboardingKickoff); got != protocol.ChatMessageKindOnboardingKickoff {
		t.Fatalf("normalizeMessageKind() = %q, want %q", got, protocol.ChatMessageKindOnboardingKickoff)
	}
}

func TestVisibleChatMessagesHidesOnboardingKickoff(t *testing.T) {
	messages := []db.ChatMessage{
		{
			Content:     "internal kickoff",
			MessageKind: protocol.ChatMessageKindOnboardingKickoff,
		},
		{
			Content:     "Hi, I'm Mika.",
			MessageKind: protocol.ChatMessageKindMessage,
		},
	}

	visible := visibleChatMessages(messages)
	if len(visible) != 1 || visible[0].Content != "Hi, I'm Mika." {
		t.Fatalf("visibleChatMessages() = %#v, want only Mika's visible reply", visible)
	}
}
