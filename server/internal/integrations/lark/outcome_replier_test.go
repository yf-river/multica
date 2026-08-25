package lark

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// stubAPIClientWithRecorder is a fake APIClient that captures the
// arguments of each outbound call so the replier tests can assert
// what landed.
type stubAPIClientWithRecorder struct {
	mu             sync.Mutex
	bindingCalls   []BindingPromptParams
	interactiveOut []SendCardParams
	textOut        []SendTextParams
	sendErr        error
	textErr        error
	bindingErr     error
}

func (s *stubAPIClientWithRecorder) SendInteractiveCard(ctx context.Context, p SendCardParams) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return "", s.sendErr
	}
	s.interactiveOut = append(s.interactiveOut, p)
	return "lark-msg-id", nil
}

func (s *stubAPIClientWithRecorder) SendTextMessage(ctx context.Context, p SendTextParams) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.textErr != nil {
		return "", s.textErr
	}
	s.textOut = append(s.textOut, p)
	return "lark-text-msg-id", nil
}

func (s *stubAPIClientWithRecorder) SendMarkdownCard(ctx context.Context, p SendMarkdownCardParams) (string, error) {
	return "lark-md-msg-id", nil
}

func (s *stubAPIClientWithRecorder) SendBindingPromptCard(ctx context.Context, p BindingPromptParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindingErr != nil {
		return s.bindingErr
	}
	s.bindingCalls = append(s.bindingCalls, p)
	return nil
}

func (s *stubAPIClientWithRecorder) GetBotInfo(ctx context.Context, creds InstallationCredentials) (BotInfo, error) {
	return BotInfo{}, nil
}

func (s *stubAPIClientWithRecorder) GetMessage(ctx context.Context, creds InstallationCredentials, messageID string) ([]LarkMessage, error) {
	return nil, nil
}
func (s *stubAPIClientWithRecorder) ListChatMessages(ctx context.Context, creds InstallationCredentials, p ListMessagesParams) ([]LarkMessage, error) {
	return nil, nil
}
func (s *stubAPIClientWithRecorder) BatchGetUsers(ctx context.Context, creds InstallationCredentials, openIDs []string) (map[string]string, error) {
	return nil, nil
}
func (s *stubAPIClientWithRecorder) AddMessageReaction(ctx context.Context, p AddReactionParams) (string, error) {
	return "stub-reaction-id", nil
}
func (s *stubAPIClientWithRecorder) DeleteMessageReaction(ctx context.Context, p DeleteReactionParams) error {
	return nil
}

func stubAgentLookup(agent db.Agent) func(context.Context, pgtype.UUID) (db.Agent, error) {
	return func(context.Context, pgtype.UUID) (db.Agent, error) { return agent, nil }
}

func newOutcomeReplierForTest(stub *stubAPIClientWithRecorder, agent db.Agent) *LarkOutcomeReplier {
	replier := NewLarkOutcomeReplier(OutcomeReplierConfig{
		APIClient:          stub,
		BindingSvc:         &BindingTokenService{},
		ResolveCredentials: testCredentials("s"),
		GetAgent:           stubAgentLookup(agent),
		PublicURL:          "https://multica.test",
	})
	replier.log = newDiscardLogger()
	return replier
}

func TestLarkOutcomeReplierAgentArchivedSendsCard(t *testing.T) {
	t.Parallel()
	stub := &stubAPIClientWithRecorder{}
	rep := newOutcomeReplierForTest(stub, db.Agent{})
	msg := InboundMessage{ChatID: "oc_chat_arch"}
	rep.Reply(context.Background(), db.LarkInstallation{Region: "feishu"}, msg, DispatchResult{Outcome: OutcomeAgentArchived})
	if len(stub.interactiveOut) != 1 {
		t.Fatalf("expected one SendInteractiveCard call, got %d", len(stub.interactiveOut))
	}
	if !strings.Contains(stub.interactiveOut[0].CardJSON, "归档") {
		t.Errorf("CardJSON should embed archived copy: %s", stub.interactiveOut[0].CardJSON)
	}
}

func TestLarkOutcomeReplierDroppedIsSilent(t *testing.T) {
	t.Parallel()
	stub := &stubAPIClientWithRecorder{}
	rep := newOutcomeReplierForTest(stub, db.Agent{})
	msg := InboundMessage{ChatID: "oc_x"}
	rep.Reply(context.Background(), db.LarkInstallation{}, msg, DispatchResult{Outcome: OutcomeDropped, DropReason: DropReasonDuplicate})
	if len(stub.interactiveOut) != 0 || len(stub.bindingCalls) != 0 {
		t.Errorf("Dropped should not trigger any APIClient call; got interactive=%d binding=%d",
			len(stub.interactiveOut), len(stub.bindingCalls))
	}
}

func TestLarkOutcomeReplierIssueCreatedSendsConfirmation(t *testing.T) {
	t.Parallel()
	stub := &stubAPIClientWithRecorder{}
	rep := newOutcomeReplierForTest(stub, db.Agent{})

	inst := db.LarkInstallation{AppID: "cli_x", Region: "feishu"}
	inst.ID = mustUUID("11111111-1111-1111-1111-111111111111")
	msg := InboundMessage{ChatID: "oc_chat_42", SenderOpenID: "ou_user"}
	rep.Reply(context.Background(), inst, msg, DispatchResult{
		Outcome:         OutcomeIngested,
		IssueID:         mustUUID("22222222-2222-2222-2222-222222222222"),
		IssueNumber:     42,
		IssueIdentifier: "MUL-42",
		IssueTitle:      "fix login bug",
	})

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.textOut) != 1 {
		t.Fatalf("expected one SendTextMessage call, got %d", len(stub.textOut))
	}
	got := stub.textOut[0]
	if got.ChatID != "oc_chat_42" {
		t.Errorf("ChatID = %q; want oc_chat_42", got.ChatID)
	}
	if !strings.Contains(got.Text, "MUL-42") {
		t.Errorf("text should embed the workspace-qualified key; got %q", got.Text)
	}
	if !strings.Contains(got.Text, "fix login bug") {
		t.Errorf("text should embed the issue title; got %q", got.Text)
	}
	if !strings.Contains(got.Text, "https://multica.test/issues/MUL-42") {
		t.Errorf("text should embed the deep link back to Multica; got %q", got.Text)
	}
	if len(stub.interactiveOut) != 0 {
		t.Errorf("issue-created confirmation must not send a card; got %d cards", len(stub.interactiveOut))
	}
}

func TestLarkOutcomeReplierOutcomeIngestedSilentWithoutIssue(t *testing.T) {
	t.Parallel()
	stub := &stubAPIClientWithRecorder{}
	rep := newOutcomeReplierForTest(stub, db.Agent{})

	rep.Reply(context.Background(), db.LarkInstallation{}, InboundMessage{ChatID: "oc"},
		DispatchResult{Outcome: OutcomeIngested})

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.textOut) != 0 || len(stub.interactiveOut) != 0 {
		t.Errorf("plain chat ingest must be silent at the replier; got text=%d cards=%d",
			len(stub.textOut), len(stub.interactiveOut))
	}
}

func mustUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		panic(err)
	}
	return u
}
