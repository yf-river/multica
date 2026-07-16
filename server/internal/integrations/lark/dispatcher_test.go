package lark

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fakeDedupRow is the test-side model of a lark_inbound_message_dedup
// row. It tracks the three pieces of state that drive the dispatcher's
// finalize logic: terminal flag (processed_at IS NOT NULL), the
// currently-live claim_token, and a counter of how many distinct
// claim_tokens have ever been minted for this message_id (used by
// tests to assert that a stale-reclaim actually rotated the token).
type fakeDedupRow struct {
	processed bool
	token     pgtype.UUID
	// rotations is the number of times claim_token has been minted
	// for this row (1 = inserted, 2+ = stale-reclaimed N-1 times).
	rotations int
}

// fakeQueries is the unit-test seam for DispatcherQueries. Each field
// is the canned response the fake returns from the corresponding
// method; ErrNoRows variants pin specific failure modes.
//
// Dedup state mirrors lark_inbound_message_dedup with owner fencing:
// each row carries a current claim_token, and Mark/Release require a
// matching token to succeed (zero rows otherwise, exactly like the
// production query). Tests pre-seed terminal rows by writing
// processed=true; tests exercising the in-flight branch write
// processed=false. The default empty map means "first delivery for
// every message_id".
type fakeQueries struct {
	installationByApp db.LarkInstallation
	installationErr   error
	userBinding       db.LarkUserBinding
	userBindingErr    error
	// userBindingByOpenID, when set, overrides userBinding per Lark open ID so a
	// test can simulate distinct senders in one chat (MUL-2645 latest-sender-wins).
	userBindingByOpenID map[string]db.LarkUserBinding
	chatSession         db.ChatSession
	chatSessionErr      error
	workspace           db.Workspace
	workspaceErr        error
	dedup               map[string]*fakeDedupRow
	dedupClaimErr       error
	dedupReclaim        bool // when true, in-flight rows are re-claimable (simulates staleness)
	nextTokenByte       byte // monotonically incremented; ensures each minted token is distinct
	calledUserBinding   int
	calledChatSession   int
	calledInstallation  int
	calledClaim         int
	calledMark          int
	calledRelease       int
}

// mintToken produces a deterministic, distinct token per call so
// tests can compare them. Production uses gen_random_uuid(); the
// fake only needs uniqueness, not randomness.
func (f *fakeQueries) mintToken() pgtype.UUID {
	f.nextTokenByte++
	return validUUID(0xA0 + f.nextTokenByte)
}

func (f *fakeQueries) GetLarkInstallationByAppID(ctx context.Context, appID string) (db.LarkInstallation, error) {
	f.calledInstallation++
	return f.installationByApp, f.installationErr
}

func (f *fakeQueries) GetLarkUserBindingByOpenID(ctx context.Context, arg db.GetLarkUserBindingByOpenIDParams) (db.LarkUserBinding, error) {
	f.calledUserBinding++
	if f.userBindingByOpenID != nil {
		if b, ok := f.userBindingByOpenID[arg.LarkOpenID]; ok {
			return b, nil
		}
	}
	return f.userBinding, f.userBindingErr
}

func (f *fakeQueries) GetChatSession(ctx context.Context, id pgtype.UUID) (db.ChatSession, error) {
	f.calledChatSession++
	return f.chatSession, f.chatSessionErr
}

// ClaimLarkInboundDedup mirrors the production query's three outcomes:
//
//   - Row not present → INSERT succeeds, mints a fresh claim_token →
//     returns the row.
//   - Row present, processed=false, dedupReclaim=true → staleness
//     fallback re-takes the claim and ROTATES the claim_token →
//     returns the row.
//   - Row present otherwise → ON CONFLICT WHERE filter excludes the
//     UPDATE → RETURNING returns 0 rows → pgx.ErrNoRows.
func (f *fakeQueries) ClaimLarkInboundDedup(ctx context.Context, arg db.ClaimLarkInboundDedupParams) (db.LarkInboundMessageDedup, error) {
	f.calledClaim++
	if f.dedupClaimErr != nil {
		return db.LarkInboundMessageDedup{}, f.dedupClaimErr
	}
	if f.dedup == nil {
		f.dedup = map[string]*fakeDedupRow{}
	}
	key := dedupKey(arg.InstallationID, arg.MessageID)
	row, exists := f.dedup[key]
	if !exists {
		token := f.mintToken()
		f.dedup[key] = &fakeDedupRow{token: token, rotations: 1}
		return db.LarkInboundMessageDedup{
			InstallationID: arg.InstallationID,
			MessageID:      arg.MessageID,
			ClaimToken:     token,
		}, nil
	}
	if !row.processed && f.dedupReclaim {
		// In-flight claim re-taken — rotate the token. This is what
		// fences the previous worker out: their saved claim_token no
		// longer matches the row's live one, so Mark/Release becomes
		// a no-op for them and (for the in-tx Mark) the chat_message
		// tx rolls back.
		row.token = f.mintToken()
		row.rotations++
		return db.LarkInboundMessageDedup{
			InstallationID: arg.InstallationID,
			MessageID:      arg.MessageID,
			ClaimToken:     row.token,
		}, nil
	}
	return db.LarkInboundMessageDedup{}, pgx.ErrNoRows
}

// dedupKey mirrors the production (installation_id, message_id) composite
// PK in the test map. Installations are not isolated by message_id alone:
// a Lark group with multiple bots installed delivers the SAME message_id
// to every bot's WS, and each one must be free to claim, evaluate
// AddressedToBot independently, and either ingest or drop as
// not_addressed_in_group.
func dedupKey(installationID pgtype.UUID, messageID string) string {
	var b [16]byte
	if installationID.Valid {
		b = installationID.Bytes
	}
	return string(b[:]) + "|" + messageID
}

// MarkLarkInboundDedupProcessed mirrors the production UPDATE: only
// the holder of the current claim_token can mark the row, and only
// while it is still in-flight (processed_at IS NULL). Mismatched token
// or already-terminal row returns 0 rows affected (and nil error) —
// the dispatcher relies on this for the in-tx ErrClaimLost path.
func (f *fakeQueries) MarkLarkInboundDedupProcessed(ctx context.Context, arg db.MarkLarkInboundDedupProcessedParams) (int64, error) {
	f.calledMark++
	if f.dedup == nil {
		return 0, nil
	}
	row, ok := f.dedup[dedupKey(arg.InstallationID, arg.MessageID)]
	if !ok {
		return 0, nil
	}
	if row.processed {
		return 0, nil
	}
	if row.token != arg.ClaimToken {
		return 0, nil
	}
	row.processed = true
	return 1, nil
}

// ReleaseLarkInboundDedup mirrors the production DELETE: only the
// holder of the current claim_token can release the row, and only
// while it is still in-flight.
func (f *fakeQueries) GetWorkspace(ctx context.Context, id pgtype.UUID) (db.Workspace, error) {
	return f.workspace, f.workspaceErr
}

func (f *fakeQueries) ReleaseLarkInboundDedup(ctx context.Context, arg db.ReleaseLarkInboundDedupParams) (int64, error) {
	f.calledRelease++
	if f.dedup == nil {
		return 0, nil
	}
	key := dedupKey(arg.InstallationID, arg.MessageID)
	row, ok := f.dedup[key]
	if !ok {
		return 0, nil
	}
	if row.processed {
		return 0, nil
	}
	if row.token != arg.ClaimToken {
		return 0, nil
	}
	delete(f.dedup, key)
	return 1, nil
}

// fakeChat is a stub ChatSessionService that records what the
// dispatcher asked of it and returns canned outcomes.
//
// When `queries` is non-nil and the dispatcher hands AppendUserMessage
// a valid ClaimToken, the stub mirrors the production in-tx Mark: it
// invokes fakeQueries.MarkLarkInboundDedupProcessed with the supplied
// token before returning success. This is what reproduces the
// stale-reclaim race in tests — if the token has been rotated by a
// concurrent Claim, the Mark matches zero rows and AppendUserMessage
// returns ErrClaimLost, exactly like the real chatSessionService's
// rolled-back transaction.
//
// `beforeAppend` is a hook fired at the top of AppendUserMessage, used
// by the stale-reclaim regression test to inject a concurrent reclaim
// between the dispatcher's Claim and AppendUserMessage's in-tx Mark.
type fakeChat struct {
	ensureID         pgtype.UUID
	ensureErr        error
	appendResult     AppendResult
	appendErr        error
	queries          *fakeQueries                  // when set, runs the in-tx Mark
	beforeAppend     func(AppendUserMessageParams) // race-injection hook
	calledEnsure     int
	calledAppend     int
	lastAppendParams AppendUserMessageParams
	lastEnsureParams EnsureChatSessionParams
}

func (f *fakeChat) EnsureChatSession(ctx context.Context, p EnsureChatSessionParams) (pgtype.UUID, error) {
	f.calledEnsure++
	f.lastEnsureParams = p
	return f.ensureID, f.ensureErr
}

func (f *fakeChat) AppendUserMessage(ctx context.Context, p AppendUserMessageParams) (AppendResult, error) {
	f.calledAppend++
	f.lastAppendParams = p
	if f.beforeAppend != nil {
		f.beforeAppend(p)
	}
	if f.appendErr != nil {
		return f.appendResult, f.appendErr
	}
	// Mirror chatSessionService.AppendUserMessage when a test supplies the
	// query fake: Mark in-tx; zero rows means stale-reclaim rotated the token.
	if f.queries != nil && p.ClaimToken.Valid && p.LarkMessageID != "" {
		rows, err := f.queries.MarkLarkInboundDedupProcessed(ctx, db.MarkLarkInboundDedupProcessedParams{
			InstallationID: p.InstallationID,
			MessageID:      p.LarkMessageID,
			ClaimToken:     p.ClaimToken,
		})
		if err != nil {
			return AppendResult{}, err
		}
		if rows == 0 {
			return AppendResult{}, ErrClaimLost
		}
	}
	return f.appendResult, nil
}

type fakeAudit struct {
	drops []AuditDropParams
	err   error
}

func (f *fakeAudit) RecordDrop(ctx context.Context, p AuditDropParams) error {
	if f.err != nil {
		return f.err
	}
	f.drops = append(f.drops, p)
	return nil
}

type fakeIssueCreator struct {
	called int
	params service.IssueCreateParams
	result service.IssueCreateResult
	err    error
}

func (f *fakeIssueCreator) Create(ctx context.Context, p service.IssueCreateParams, _ service.IssueCreateOpts) (service.IssueCreateResult, error) {
	f.called++
	f.params = p
	return f.result, f.err
}

type fakeEnqueuer struct {
	called           int
	task             db.AgentTaskQueue
	err              error
	lastInitiatorUID pgtype.UUID
}

func (f *fakeEnqueuer) EnqueueChatTask(ctx context.Context, _ db.ChatSession, initiatorUserID pgtype.UUID) (db.AgentTaskQueue, error) {
	f.called++
	f.lastInitiatorUID = initiatorUserID
	return f.task, f.err
}

// validUUID builds a deterministic Valid pgtype.UUID from the supplied
// byte. Useful for distinguishing IDs in assertions.
func validUUID(b byte) pgtype.UUID {
	var u pgtype.UUID
	for i := range u.Bytes {
		u.Bytes[i] = b
	}
	u.Valid = true
	return u
}

func activeInstallation() db.LarkInstallation {
	return db.LarkInstallation{
		ID:              validUUID(0x11),
		WorkspaceID:     validUUID(0x22),
		AgentID:         validUUID(0x33),
		InstallerUserID: validUUID(0x99),
		Status:          installationStatusActive,
	}
}

// seedDedupKey composes a fake-table key for the default activeInstallation
// fixture (installation_id = validUUID(0x11)). Pre-seeded dedup rows in
// dispatcher tests use this to satisfy the new (installation_id,
// message_id) composite PK.
func seedDedupKey(messageID string) string {
	return dedupKey(validUUID(0x11), messageID)
}

func boundUser() db.LarkUserBinding {
	return db.LarkUserBinding{
		ID:             validUUID(0x44),
		WorkspaceID:    validUUID(0x22),
		MulticaUserID:  validUUID(0x55),
		InstallationID: validUUID(0x11),
		LarkOpenID:     "ou_user_a",
	}
}

type dispatcherFixture struct {
	dispatcher *Dispatcher
	queries    *fakeQueries
	chat       *fakeChat
	audit      *fakeAudit
	issues     *fakeIssueCreator
	enqueuer   *fakeEnqueuer
}

func newDispatcherFixture() *dispatcherFixture {
	installation := activeInstallation()
	sessionID := validUUID(0x66)
	queries := &fakeQueries{
		installationByApp: installation,
		userBinding:       boundUser(),
		chatSession:       db.ChatSession{ID: sessionID, AgentID: installation.AgentID},
		workspace:         db.Workspace{ID: installation.WorkspaceID, IssuePrefix: "MUL"},
	}
	chat := &fakeChat{ensureID: sessionID}
	audit := &fakeAudit{}
	issues := &fakeIssueCreator{}
	enqueuer := &fakeEnqueuer{}
	return &dispatcherFixture{
		queries:  queries,
		chat:     chat,
		audit:    audit,
		issues:   issues,
		enqueuer: enqueuer,
		dispatcher: &Dispatcher{
			Queries:         queries,
			Chat:            chat,
			RecordDrop:      audit.RecordDrop,
			CreateIssue:     issues.Create,
			EnqueueChatTask: enqueuer.EnqueueChatTask,
			FlushReply:      func(context.Context, db.LarkInstallation, InboundMessage, DispatchResult) {},
		},
	}
}

func TestDispatcher_UnknownAppDropped(t *testing.T) {
	f := newDispatcherFixture()
	f.queries.installationErr = pgx.ErrNoRows

	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:     "missing",
		EventType: "im.message.receive_v1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeDropped || res.DropReason != DropReasonInvalidEvent {
		t.Fatalf("unexpected outcome: %+v", res)
	}
	if len(f.audit.drops) != 1 || f.audit.drops[0].Reason != DropReasonInvalidEvent {
		t.Fatalf("expected one invalid_event audit row, got %+v", f.audit.drops)
	}
	if f.audit.drops[0].InstallationID.Valid {
		t.Fatalf("audit row should omit installation_id for unknown app: %+v", f.audit.drops[0])
	}
}

func TestDispatcher_UnknownAppAuditFailureIsRetryable(t *testing.T) {
	auditErr := errors.New("audit unavailable")
	f := newDispatcherFixture()
	f.queries.installationErr = pgx.ErrNoRows
	f.audit.err = auditErr

	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:     "missing",
		EventType: "im.message.receive_v1",
		MessageID: "msg-unknown-audit-failure",
	})
	if !errors.Is(err, auditErr) {
		t.Fatalf("expected retryable audit error, got result=%+v err=%v", res, err)
	}
}

func TestDispatcher_RevokedInstallationDropped(t *testing.T) {
	f := newDispatcherFixture()
	f.queries.installationByApp.Status = installationStatusRevoked

	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{AppID: "ok"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.DropReason != DropReasonRevokedInstallation {
		t.Fatalf("got drop reason %q", res.DropReason)
	}
	if len(f.audit.drops) != 1 || f.audit.drops[0].Reason != DropReasonRevokedInstallation {
		t.Fatalf("audit drops: %+v", f.audit.drops)
	}
}

func TestDispatcher_GroupWithoutMentionDropped(t *testing.T) {
	f := newDispatcherFixture()

	res, _ := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:          "ok",
		ChatType:       ChatTypeGroup,
		AddressedToBot: false,
	})
	if res.DropReason != DropReasonNotAddressedInGroup {
		t.Fatalf("got drop reason %q", res.DropReason)
	}
	if f.queries.calledUserBinding != 0 {
		t.Fatalf("identity check should be skipped before group filter, got %d calls", f.queries.calledUserBinding)
	}
}

func TestDispatcher_GroupDropAuditFailureReleasesClaim(t *testing.T) {
	auditErr := errors.New("audit unavailable")
	f := newDispatcherFixture()
	f.audit.err = auditErr

	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:          "ok",
		ChatType:       ChatTypeGroup,
		AddressedToBot: false,
		MessageID:      "msg-group-audit-failure",
	})
	if !errors.Is(err, auditErr) {
		t.Fatalf("expected retryable audit error, got result=%+v err=%v", res, err)
	}
	if f.queries.calledRelease != 1 {
		t.Fatalf("audit failure must release claim, release calls=%d", f.queries.calledRelease)
	}
	if _, exists := f.queries.dedup[seedDedupKey("msg-group-audit-failure")]; exists {
		t.Fatalf("audit failure left a dedup blocker: %+v", f.queries.dedup)
	}
}

func TestDispatcher_UnboundUserAsksForBinding(t *testing.T) {
	f := newDispatcherFixture()
	f.queries.userBindingErr = pgx.ErrNoRows

	res, _ := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
	})
	if res.Outcome != OutcomeNeedsBinding {
		t.Fatalf("expected OutcomeNeedsBinding, got %q", res.Outcome)
	}
	if res.DropReason != DropReasonUnboundUser {
		t.Fatalf("expected unbound_user drop reason, got %q", res.DropReason)
	}
	if res.SenderOpenID != "ou_user_a" {
		t.Fatalf("sender propagation broken: %q", res.SenderOpenID)
	}
	if len(f.audit.drops) != 1 || f.audit.drops[0].Reason != DropReasonUnboundUser {
		t.Fatalf("expected one unbound_user audit row, got %+v", f.audit.drops)
	}
}

func TestDispatcher_UnboundAuditFailureReleasesClaim(t *testing.T) {
	auditErr := errors.New("audit unavailable")
	f := newDispatcherFixture()
	f.queries.userBindingErr = pgx.ErrNoRows
	f.audit.err = auditErr

	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		MessageID:    "msg-unbound-audit-failure",
	})
	if !errors.Is(err, auditErr) {
		t.Fatalf("expected retryable audit error, got result=%+v err=%v", res, err)
	}
	if f.queries.calledRelease != 1 {
		t.Fatalf("audit failure must release claim, release calls=%d", f.queries.calledRelease)
	}
	if _, exists := f.queries.dedup[seedDedupKey("msg-unbound-audit-failure")]; exists {
		t.Fatalf("audit failure left a dedup blocker: %+v", f.queries.dedup)
	}
}

func TestDispatcher_PlainMessageEnqueuesTask(t *testing.T) {
	f := newDispatcherFixture()
	f.enqueuer.task = db.AgentTaskQueue{ID: validUUID(0x77)}

	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "hi bot",
		MessageID:    "msg-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeIngested {
		t.Fatalf("expected ingested, got %q", res.Outcome)
	}
	// Drain the pending trigger as graceful shutdown does.
	f.dispatcher.FlushPendingRuns()
	if f.enqueuer.called != 1 {
		t.Fatalf("expected exactly one EnqueueChatTask at flush; called=%d", f.enqueuer.called)
	}
	// For p2p the session creator should be the bound user, not the
	// installer — verifies the chat-type branch in Handle.
	if f.chat.lastEnsureParams.Sender != f.queries.userBinding.MulticaUserID {
		t.Fatalf("p2p session creator should be sender; got %+v", f.chat.lastEnsureParams.Sender)
	}
	// The task initiator is also the sender (MUL-2645).
	if f.enqueuer.lastInitiatorUID != f.queries.userBinding.MulticaUserID {
		t.Fatalf("p2p task initiator should be sender; got %+v want %+v",
			f.enqueuer.lastInitiatorUID, f.queries.userBinding.MulticaUserID)
	}
}

func TestDispatcher_GroupMessageUsesInstallerAsCreator(t *testing.T) {
	f := newDispatcherFixture()
	f.enqueuer.task = db.AgentTaskQueue{ID: validUUID(0x77)}

	_, _ = f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:          "ok",
		ChatType:       ChatTypeGroup,
		AddressedToBot: true,
		SenderOpenID:   "ou_user_a",
		Body:           "hey",
		MessageID:      "msg-g",
	})
	if f.chat.lastEnsureParams.Sender != f.queries.installationByApp.InstallerUserID {
		t.Fatalf("group session creator should be installer; got %+v want %+v",
			f.chat.lastEnsureParams.Sender, f.queries.installationByApp.InstallerUserID)
	}
}

// TestDispatcher_GroupMessageEnqueuesWithSenderAsInitiator is the MUL-2645
// review regression: in a Lark group chat the session creator is the installer,
// but the TASK INITIATOR must be the actual message sender. Before the fix the
// claim derived the initiator from chat_session.creator_id (= installer), so
// every group member appeared to the agent as the installer. The dispatcher now
// passes the sender's MulticaUserID to EnqueueChatTask; this asserts that the
// enqueued initiator is the sender (boundUser), NOT the installer.
func TestDispatcher_GroupMessageEnqueuesWithSenderAsInitiator(t *testing.T) {
	f := newDispatcherFixture()
	f.enqueuer.task = db.AgentTaskQueue{ID: validUUID(0x77)}

	_, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:          "ok",
		ChatType:       ChatTypeGroup,
		AddressedToBot: true,
		SenderOpenID:   "ou_user_a",
		Body:           "hey bot",
		MessageID:      "msg-g2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f.dispatcher.FlushPendingRuns()
	if f.enqueuer.called != 1 {
		t.Fatalf("expected exactly one EnqueueChatTask at flush; called=%d", f.enqueuer.called)
	}
	// Session creator stays the installer (member-churn stability)...
	if f.chat.lastEnsureParams.Sender != f.queries.installationByApp.InstallerUserID {
		t.Fatalf("group session creator should be installer; got %+v", f.chat.lastEnsureParams.Sender)
	}
	// ...but the task initiator is the SENDER, not the installer.
	if f.enqueuer.lastInitiatorUID != f.queries.userBinding.MulticaUserID {
		t.Fatalf("group task initiator should be the sender; got %+v want %+v",
			f.enqueuer.lastInitiatorUID, f.queries.userBinding.MulticaUserID)
	}
	if f.enqueuer.lastInitiatorUID == f.queries.installationByApp.InstallerUserID {
		t.Fatalf("group task initiator must NOT be the installer (%+v)", f.queries.installationByApp.InstallerUserID)
	}
}

func TestDispatcher_DedupHitDoesNotEnqueue(t *testing.T) {
	f := newDispatcherFixture()
	f.queries.dedup = map[string]*fakeDedupRow{seedDedupKey("msg-dup"): {processed: true, token: validUUID(0xAB)}}

	res, _ := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "replay",
		MessageID:    "msg-dup",
	})
	if res.Outcome != OutcomeDropped || res.DropReason != DropReasonDuplicate {
		t.Fatalf("expected duplicate drop, got %+v", res)
	}
	if f.enqueuer.called != 0 {
		t.Fatalf("dedup hit must not enqueue task, called=%d", f.enqueuer.called)
	}
	if f.chat.calledEnsure != 0 || f.chat.calledAppend != 0 {
		t.Fatalf("dedup hit must short-circuit before chat lookup; ensure=%d append=%d",
			f.chat.calledEnsure, f.chat.calledAppend)
	}
	if f.queries.calledUserBinding != 0 {
		t.Fatalf("dedup hit must short-circuit before identity check, got %d binding calls",
			f.queries.calledUserBinding)
	}
	if len(f.audit.drops) != 1 || f.audit.drops[0].Reason != DropReasonDuplicate {
		t.Fatalf("expected duplicate audit row, got %+v", f.audit.drops)
	}
}

// TestDispatcher_DedupBeforeGroupFilter pins the §4.3 ordering: a
// replayed group event that was NOT addressed to the Bot must NOT
// re-write a not_addressed_in_group audit row on every reconnect, and
// must NOT re-trigger any binding-prompt side effect. The top-level
// dedup gate is what guarantees this; before this fix the group
// filter ran first and unbounded replays produced unbounded audit
// noise + reply-card spam.
func TestDispatcher_DedupBeforeGroupFilter(t *testing.T) {
	f := newDispatcherFixture()
	f.queries.dedup = map[string]*fakeDedupRow{seedDedupKey("msg-replay"): {processed: true, token: validUUID(0xAB)}}

	res, _ := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:          "ok",
		ChatType:       ChatTypeGroup,
		AddressedToBot: false,
		MessageID:      "msg-replay",
	})
	if res.DropReason != DropReasonDuplicate {
		t.Fatalf("dedup must beat group filter; got drop reason %q", res.DropReason)
	}
	if len(f.audit.drops) != 1 || f.audit.drops[0].Reason != DropReasonDuplicate {
		t.Fatalf("expected exactly one duplicate audit row, got %+v", f.audit.drops)
	}
}

// TestDispatcher_DedupIsScopedPerInstallation pins MUL-2671's multi-bot
// invariant: in a Lark group with TWO Multica bots installed, the
// same Lark message_id arrives at both WS supervisors and each one
// MUST be free to claim, evaluate AddressedToBot independently, and
// either ingest or drop. Before the (installation_id, message_id)
// composite PK landed, whichever WS claimed first would mark the
// shared row terminal and the bot that was actually @-ed would lose
// to dedup before its filter ran — every @ to the "second" bot
// silently disappeared.
func TestDispatcher_DedupIsScopedPerInstallation(t *testing.T) {
	f := newDispatcherFixture()
	f.queries.installationByApp.ID = validUUID(0x12)
	f.queries.dedup = map[string]*fakeDedupRow{
		dedupKey(validUUID(0x11), "msg-shared"): {processed: true, token: validUUID(0xAB)},
	}

	res, _ := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:          "ok",
		ChatType:       ChatTypeGroup,
		AddressedToBot: false, // simulate "not @-ed FROM this bot's perspective"
		MessageID:      "msg-shared",
	})
	if res.DropReason != DropReasonNotAddressedInGroup {
		t.Fatalf("composite-key dedup miss must let group filter run, got drop reason %q", res.DropReason)
	}
	if _, ok := f.queries.dedup[dedupKey(f.queries.installationByApp.ID, "msg-shared")]; !ok {
		t.Fatalf("expected a fresh claim row for installation 0x12; got %v", f.queries.dedup)
	}
}

// TestDispatcher_DedupBeforeIdentityCheck pins the same ordering for
// unbound users: a replayed event from an unbound open_id must not
// re-fire the OutcomeNeedsBinding path on every reconnect — that
// would spam the user with binding-prompt cards.
func TestDispatcher_DedupBeforeIdentityCheck(t *testing.T) {
	f := newDispatcherFixture()
	f.queries.userBindingErr = pgx.ErrNoRows
	f.queries.dedup = map[string]*fakeDedupRow{seedDedupKey("msg-replay"): {processed: true, token: validUUID(0xAB)}}

	res, _ := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		MessageID:    "msg-replay",
	})
	if res.Outcome != OutcomeDropped || res.DropReason != DropReasonDuplicate {
		t.Fatalf("dedup must beat identity check; got %+v", res)
	}
	if f.queries.calledUserBinding != 0 {
		t.Fatalf("identity check must not run for a deduped replay, got %d calls",
			f.queries.calledUserBinding)
	}
}

func TestDispatcher_IssueCommandCreatesIssue(t *testing.T) {
	f := newDispatcherFixture()
	f.chat.appendResult.IssueCommand = &IssueCommand{Title: "ship it", Description: "ship the thing"}
	f.issues.result = service.IssueCreateResult{Issue: db.Issue{
		ID:     validUUID(0x88),
		Number: 42,
		Title:  "ship it",
	}}

	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "/issue ship it\nship the thing",
		MessageID:    "msg-ic",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.issues.called != 1 {
		t.Fatalf("expected IssueService.Create called once, got %d", f.issues.called)
	}
	if f.issues.params.Title != "ship it" || f.issues.params.Description.String != "ship the thing" {
		t.Fatalf("wrong issue params: %+v", f.issues.params)
	}
	if f.issues.params.OriginType.String != originLarkChat {
		t.Fatalf("origin_type should be lark_chat, got %q", f.issues.params.OriginType.String)
	}
	if !f.issues.params.AssigneeType.Valid || f.issues.params.AssigneeType.String != "agent" ||
		f.issues.params.AssigneeID != f.queries.installationByApp.AgentID {
		t.Fatalf("assignee should default to the installation's agent: %+v", f.issues.params)
	}
	if !res.IssueID.Valid || res.IssueNumber != 42 {
		t.Fatalf("issue id/number not propagated: %+v", res)
	}
	// IssueIdentifier and IssueTitle are how the OutcomeReplier knows
	// what to put in the "Created [MUL-42] ship it" confirmation
	// message. They MUST be populated whenever a /issue command
	// produced a row.
	if res.IssueIdentifier != "MUL-42" {
		t.Fatalf("issue identifier should reflect workspace prefix; got %q", res.IssueIdentifier)
	}
	if res.IssueTitle != "ship it" {
		t.Fatalf("issue title should be propagated; got %q", res.IssueTitle)
	}
}

// TestDispatcher_IssueIdentifierFallsBackToNumberOnWorkspaceLookupErr
// pins the degrade-gracefully behaviour: a Postgres blip on the
// workspace row should NOT silently drop the issue-created
// confirmation. We emit "#42" instead of "MUL-42" in that case so
// the user still sees that the issue was created.
func TestDispatcher_IssueIdentifierFallsBackToNumberOnWorkspaceLookupErr(t *testing.T) {
	f := newDispatcherFixture()
	f.queries.workspaceErr = errors.New("workspace lookup failed")
	f.chat.appendResult.IssueCommand = &IssueCommand{Title: "fallback path"}
	f.issues.result = service.IssueCreateResult{Issue: db.Issue{
		ID:     validUUID(0x88),
		Number: 7,
		Title:  "fallback path",
	}}

	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "/issue fallback path",
		MessageID:    "msg-fallback",
	})
	if err != nil {
		t.Fatalf("workspace lookup error must NOT abort dispatch; got %v", err)
	}
	if res.IssueIdentifier != "#7" {
		t.Errorf("expected fallback identifier '#7'; got %q", res.IssueIdentifier)
	}
}

func TestDispatcher_EmptyTitleSurfacesError(t *testing.T) {
	f := newDispatcherFixture()
	f.chat.appendResult.IssueCommand = &IssueCommand{}

	_, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "/issue",
		MessageID:    "msg-empty",
	})
	if !errors.Is(err, ErrEmptyIssueTitle) {
		t.Fatalf("expected ErrEmptyIssueTitle wrapped, got %v", err)
	}
	if f.issues.called != 0 {
		t.Fatalf("IssueService.Create must not run when title is empty")
	}
}

// captureReply is a FlushReply seam: it records every offline/archived
// notice the dispatcher emits at flush time so tests can assert what the
// user-facing card would say.
type captureReply struct {
	count   int
	results []DispatchResult
}

func (c *captureReply) reply(_ context.Context, _ db.LarkInstallation, _ InboundMessage, res DispatchResult) {
	c.count++
	c.results = append(c.results, res)
}

func TestDispatcher_AgentArchivedRepliesAtFlush(t *testing.T) {
	f := newDispatcherFixture()
	f.enqueuer.err = service.ErrChatTaskAgentArchived
	cap := &captureReply{}
	f.dispatcher.FlushReply = cap.reply

	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "hi",
		MessageID:    "msg-arch",
	})
	if err != nil {
		t.Fatalf("archived path should not return error, got %v", err)
	}
	if res.Outcome != OutcomeIngested {
		t.Fatalf("synchronous outcome must be ingested, got %q", res.Outcome)
	}
	f.dispatcher.FlushPendingRuns()
	if cap.count != 1 || cap.results[0].Outcome != OutcomeAgentArchived {
		t.Fatalf("expected OutcomeAgentArchived at flush, got count=%d results=%+v", cap.count, cap.results)
	}
}

func TestDispatcher_FlushInfraFailureIsNotReplied(t *testing.T) {
	f := newDispatcherFixture()
	f.enqueuer.err = errors.New("create chat task: connection refused")
	cap := &captureReply{}
	f.dispatcher.FlushReply = cap.reply

	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "hi",
		MessageID:    "msg-infra",
	})
	if err != nil {
		t.Fatalf("flush infra failure must not surface from Handle, got %v", err)
	}
	if res.Outcome != OutcomeIngested {
		t.Fatalf("synchronous outcome must be ingested, got %q", res.Outcome)
	}
	f.dispatcher.FlushPendingRuns()
	if f.enqueuer.called != 1 {
		t.Fatalf("flush must attempt EnqueueChatTask once; called=%d", f.enqueuer.called)
	}
	if cap.count != 0 {
		t.Fatalf("infra failure must not emit any offline/archived card; replies=%d", cap.count)
	}
}

func TestDispatcher_DebounceCoalescesRunTrigger(t *testing.T) {
	fixture := newDispatcherFixture()
	fixture.enqueuer.task = db.AgentTaskQueue{ID: validUUID(0x77)}
	timers := &fakeTimerFactory{}
	fixture.dispatcher.batcher = newTestBatcher(timers)

	send := func(id string) {
		res, err := fixture.dispatcher.Handle(context.Background(), InboundMessage{
			AppID:        "ok",
			ChatType:     ChatTypeP2P,
			SenderOpenID: "ou_user_a",
			Body:         "hi",
			MessageID:    id,
		})
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", id, err)
		}
		if res.Outcome != OutcomeIngested {
			t.Fatalf("expected ingested for %s, got %q", id, res.Outcome)
		}
	}

	send("m1")
	send("m2")

	if fixture.enqueuer.called != 0 {
		t.Fatalf("run trigger must be debounced; enqueue called=%d before window closed", fixture.enqueuer.called)
	}
	if got := pendingBatchCount(&fixture.dispatcher.batcher); got != 1 {
		t.Fatalf("both messages share one session window; pending=%d", got)
	}

	timers.fireArmed()
	if fixture.enqueuer.called != 1 {
		t.Fatalf("a coalesced burst must enqueue exactly once; called=%d", fixture.enqueuer.called)
	}

	send("m3")
	timers.fireArmed()
	if fixture.enqueuer.called != 2 {
		t.Fatalf("a message after the window must start a new run; called=%d", fixture.enqueuer.called)
	}
}

// TestDispatcher_LatestSenderWinsAsInitiator pins the MUL-2645 batching
// decision: when several people speak in one chat within a single silence
// window, the coalesced run's initiator is the LAST sender. The debouncer keeps
// only the latest scheduled flush per session, and each flush captures its own
// message's sender, so the final enqueue carries the last sender's identity.
func TestDispatcher_LatestSenderWinsAsInitiator(t *testing.T) {
	fixture := newDispatcherFixture()
	alice := boundUser() // MulticaUserID 0x55, open id ou_alice below
	bob := boundUser()
	bob.MulticaUserID = validUUID(0xBB)
	fixture.queries.userBindingByOpenID = map[string]db.LarkUserBinding{"ou_alice": alice, "ou_bob": bob}
	fixture.enqueuer.task = db.AgentTaskQueue{ID: validUUID(0x77)}
	timers := &fakeTimerFactory{}
	fixture.dispatcher.batcher = newTestBatcher(timers)

	send := func(openID, msgID string) {
		if _, err := fixture.dispatcher.Handle(context.Background(), InboundMessage{
			AppID:        "ok",
			ChatType:     ChatTypeP2P,
			SenderOpenID: OpenID(openID),
			Body:         "hi",
			MessageID:    msgID,
		}); err != nil {
			t.Fatalf("unexpected error for %s: %v", msgID, err)
		}
	}

	send("ou_alice", "m1") // Alice first...
	send("ou_bob", "m2")   // ...then Bob, within the same window.
	timers.fireArmed()     // window closes → one coalesced run

	if fixture.enqueuer.called != 1 {
		t.Fatalf("a coalesced burst must enqueue exactly once; called=%d", fixture.enqueuer.called)
	}
	if fixture.enqueuer.lastInitiatorUID != bob.MulticaUserID {
		t.Fatalf("latest sender (Bob) should be the initiator; got %+v want %+v",
			fixture.enqueuer.lastInitiatorUID, bob.MulticaUserID)
	}
}

func TestDispatcher_EnsureChatSessionFailureReleasesClaim(t *testing.T) {
	f := newDispatcherFixture()
	f.chat.ensureErr = errors.New("ensure chat session: connection refused")
	f.chat.queries = f.queries

	_, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "hi",
		MessageID:    "msg-retry",
	})
	if err == nil {
		t.Fatalf("first attempt should surface ensure-chat-session error, got nil")
	}
	if f.queries.calledMark != 0 {
		t.Fatalf("must not mark processed when no durable side effect landed; calledMark=%d", f.queries.calledMark)
	}
	if f.queries.calledRelease != 1 {
		t.Fatalf("must release the claim on pre-durable infra error; calledRelease=%d", f.queries.calledRelease)
	}
	if _, present := f.queries.dedup[seedDedupKey("msg-retry")]; present {
		t.Fatalf("release must delete the in-flight claim row; dedup=%+v", f.queries.dedup)
	}

	f.chat.ensureErr = nil
	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "hi",
		MessageID:    "msg-retry",
	})
	if err != nil {
		t.Fatalf("retry should succeed, got %v", err)
	}
	if res.Outcome != OutcomeIngested {
		t.Fatalf("retry must ingest; got outcome=%q reason=%q", res.Outcome, res.DropReason)
	}
	if f.chat.calledAppend != 1 {
		t.Fatalf("retry must reach AppendUserMessage; calledAppend=%d", f.chat.calledAppend)
	}
	if f.queries.calledMark != 1 {
		t.Fatalf("retry must mark processed; calledMark=%d", f.queries.calledMark)
	}
	if row, ok := f.queries.dedup[seedDedupKey("msg-retry")]; !ok || !row.processed {
		t.Fatalf("retry must finalize claim as processed; dedup=%+v", f.queries.dedup)
	}

	f.queries.calledClaim = 0
	f.chat.calledAppend = 0
	res, err = f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "hi",
		MessageID:    "msg-retry",
	})
	if err != nil {
		t.Fatalf("post-success replay should not error, got %v", err)
	}
	if res.Outcome != OutcomeDropped || res.DropReason != DropReasonDuplicate {
		t.Fatalf("post-success replay must duplicate-drop; got %+v", res)
	}
	if f.chat.calledAppend != 0 {
		t.Fatalf("post-success replay must not re-append; calledAppend=%d", f.chat.calledAppend)
	}
}

func TestDispatcher_AppendUserMessageFailureReleasesClaim(t *testing.T) {
	f := newDispatcherFixture()
	f.chat.appendErr = errors.New("create chat message: connection refused")

	_, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "hi",
		MessageID:    "msg-append-retry",
	})
	if err == nil {
		t.Fatalf("first attempt should surface append error, got nil")
	}
	if f.queries.calledMark != 0 {
		t.Fatalf("must not mark processed when AppendUserMessage rolled back; calledMark=%d", f.queries.calledMark)
	}
	if f.queries.calledRelease != 1 {
		t.Fatalf("must release the claim; calledRelease=%d", f.queries.calledRelease)
	}

	f.chat.appendErr = nil
	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "hi",
		MessageID:    "msg-append-retry",
	})
	if err != nil {
		t.Fatalf("retry should succeed, got %v", err)
	}
	if res.Outcome != OutcomeIngested {
		t.Fatalf("retry must ingest; got %+v", res)
	}
	if f.chat.calledAppend != 2 {
		t.Fatalf("expected exactly two append attempts (1 failed + 1 retry); calledAppend=%d", f.chat.calledAppend)
	}
}

func TestDispatcher_DurableErrorMarksClaim(t *testing.T) {
	f := newDispatcherFixture()
	f.chat.appendResult.IssueCommand = &IssueCommand{Title: "boom"}
	f.chat.queries = f.queries
	issueErr := errors.New("create issue: connection refused")
	f.issues.err = issueErr

	_, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "/issue boom",
		MessageID:    "msg-durable-err",
	})
	if !errors.Is(err, issueErr) {
		t.Fatalf("expected post-append durable error to propagate, got %v", err)
	}
	if f.queries.calledRelease != 0 {
		t.Fatalf("must not release: chat_message already committed; calledRelease=%d", f.queries.calledRelease)
	}
	if f.queries.calledMark != 1 {
		t.Fatalf("must mark processed: chat_message committed before the enqueue error; calledMark=%d", f.queries.calledMark)
	}
	if row, ok := f.queries.dedup[seedDedupKey("msg-durable-err")]; !ok || !row.processed {
		t.Fatalf("dedup row must end up processed=true; got %+v", f.queries.dedup)
	}
}

// TestDispatcher_DropMarksClaim pins that audit-drop branches (group
// filter, unbound user) finalize their claim as processed, so a
// reconnect replay does NOT re-write the audit row or re-fire any
// binding-prompt side effect. This is the "no audit / card spam"
// invariant from §4.3.
func TestDispatcher_DropMarksClaim(t *testing.T) {
	f := newDispatcherFixture()
	f.queries.userBindingErr = pgx.ErrNoRows

	_, _ = f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		MessageID:    "msg-unbound",
	})
	if f.queries.calledMark != 1 {
		t.Fatalf("unbound-user drop must mark claim processed; calledMark=%d", f.queries.calledMark)
	}
	if f.queries.calledRelease != 0 {
		t.Fatalf("unbound-user drop must not release; calledRelease=%d", f.queries.calledRelease)
	}
}

// TestDispatcher_EmptyMessageIDSkipsDedup pins that non-message
// events (no MessageID) bypass dedup entirely — there is no key to
// deduplicate by, and the dispatcher must not call Claim / Mark /
// Release for them.
func TestDispatcher_EmptyMessageIDSkipsDedup(t *testing.T) {
	f := newDispatcherFixture()

	_, _ = f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:    "ok",
		ChatType: ChatTypeGroup, // group filter drops it
		// MessageID intentionally empty
	})
	if f.queries.calledClaim != 0 || f.queries.calledMark != 0 || f.queries.calledRelease != 0 {
		t.Fatalf("empty MessageID must skip dedup entirely; claim=%d mark=%d release=%d",
			f.queries.calledClaim, f.queries.calledMark, f.queries.calledRelease)
	}
}

// TestDispatcher_InFlightClaimDropsReplay covers the "another worker
// is processing" branch: a fresh in-flight claim (processed=false,
// not yet stale) must duplicate-drop a concurrent replay, NOT
// re-process. This is the protection against two replicas
// simultaneously consuming the same Lark event during a brief
// double-leased window.
func TestDispatcher_InFlightClaimDropsReplay(t *testing.T) {
	f := newDispatcherFixture()
	f.queries.dedup = map[string]*fakeDedupRow{seedDedupKey("msg-inflight"): {token: validUUID(0xAB)}}

	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "race",
		MessageID:    "msg-inflight",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeDropped || res.DropReason != DropReasonDuplicate {
		t.Fatalf("in-flight claim must drop the replay; got %+v", res)
	}
	if f.chat.calledEnsure != 0 || f.chat.calledAppend != 0 {
		t.Fatalf("in-flight drop must short-circuit before chat lookup; ensure=%d append=%d",
			f.chat.calledEnsure, f.chat.calledAppend)
	}
}

// TestDispatcher_StaleInFlightClaimReclaimable covers the
// crash-recovery branch: an in-flight claim older than the staleness
// TTL must be re-takeable so a process crash mid-pipeline does not
// leave the message stuck forever.
func TestDispatcher_StaleInFlightClaimReclaimable(t *testing.T) {
	f := newDispatcherFixture()
	f.queries.dedup = map[string]*fakeDedupRow{seedDedupKey("msg-stale"): {token: validUUID(0xAB)}}
	f.queries.dedupReclaim = true
	f.chat.queries = f.queries

	res, err := f.dispatcher.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "after-crash retry",
		MessageID:    "msg-stale",
	})
	if err != nil {
		t.Fatalf("stale-claim retry should succeed, got %v", err)
	}
	if res.Outcome != OutcomeIngested {
		t.Fatalf("stale-claim retry must ingest; got %+v", res)
	}
	if f.queries.calledMark != 1 {
		t.Fatalf("stale-claim retry must mark processed; calledMark=%d", f.queries.calledMark)
	}
}

// TestDispatcher_StaleReclaimRaceDoesNotDoubleWrite is the regression
// for Elon's first must-fix on PR #3277 dedup: worker A claims a
// dedup row at t=0 with token T_A, runs slowly past the 60-second
// staleness TTL, and is still alive. Worker B (a replay) sees the row
// as stale-reclaimable, takes the claim, rotates the token to T_B,
// and runs the full ingest pipeline. A subsequently reaches its in-tx
// Mark with the old T_A. WITHOUT owner fencing both A and B would
// commit a chat_message for the same Lark message_id — the bug Elon
// flagged. WITH owner fencing A's Mark matches zero rows, the in-tx
// Mark returns ErrClaimLost, A's chat_message+session transaction
// rolls back, and B is the sole writer.
//
// The test reproduces this by inverting the timeline: worker A is
// Handle()'s active call, and worker B is injected by the
// `beforeAppend` hook, which rotates the row's claim_token between
// the dispatcher's ClaimLarkInboundDedup call and AppendUserMessage's
// in-tx Mark. The hook fires exactly once so the second Handle()
// continues normally.
func TestDispatcher_StaleReclaimRaceDoesNotDoubleWrite(t *testing.T) {
	sessionID := validUUID(0x66)
	queries := &fakeQueries{
		installationByApp: activeInstallation(),
		userBinding:       boundUser(),
		chatSession:       db.ChatSession{ID: sessionID, AgentID: validUUID(0x33)},
	}
	chat := &fakeChat{ensureID: sessionID, appendResult: AppendResult{}}
	chat.queries = queries // model the production in-tx Mark
	d := &Dispatcher{
		Queries:         queries,
		Chat:            chat,
		RecordDrop:      (&fakeAudit{}).RecordDrop,
		EnqueueChatTask: (&fakeEnqueuer{task: db.AgentTaskQueue{ID: validUUID(0x77)}}).EnqueueChatTask,
	}

	// Inject worker B's reclaim. The hook fires while worker A's
	// AppendUserMessage is running with its original (now-stale)
	// token; ClaimLarkInboundDedup with dedupReclaim=true rotates the
	// row's claim_token to T_B. When fakeChat then attempts the in-tx
	// Mark with T_A it must match zero rows and return ErrClaimLost.
	raceFired := false
	originalToken := pgtype.UUID{}
	chat.beforeAppend = func(p AppendUserMessageParams) {
		if raceFired {
			return
		}
		raceFired = true
		originalToken = p.ClaimToken
		// Make the existing in-flight row reclaimable, then have
		// worker B re-Claim. This rotates claim_token under A's feet.
		queries.dedupReclaim = true
		if _, err := queries.ClaimLarkInboundDedup(context.Background(), db.ClaimLarkInboundDedupParams{
			InstallationID: p.InstallationID,
			MessageID:      p.LarkMessageID,
		}); err != nil {
			t.Fatalf("worker-B reclaim setup failed: %v", err)
		}
		// Switch reclaim off so the dispatcher-level retry path (the
		// second Handle below) doesn't keep rotating the token.
		queries.dedupReclaim = false
	}

	res, err := d.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "slow-worker A",
		MessageID:    "msg-race",
	})
	if err != nil {
		t.Fatalf("stale-reclaim race should surface as duplicate drop, not error; got %v", err)
	}
	if res.Outcome != OutcomeDropped || res.DropReason != DropReasonDuplicate {
		t.Fatalf("worker A must observe duplicate drop after losing claim; got %+v", res)
	}

	// Critical regression assertion: worker A must NOT have committed
	// a chat_message. AppendUserMessage was called (race hook fired),
	// but its in-tx Mark matched zero rows, so the tx rolled back.
	// The "chat_message committed" signal in this fake is appendResult
	// — fakeChat returning success would have made calledAppend bump
	// AND the row would have been marked under A's token; instead A
	// got ErrClaimLost. To pin "no double write" we check that the
	// row's processed_at was set by worker B's path (the rotated
	// token, T_B), not by worker A's old token (T_A).
	row, ok := queries.dedup[seedDedupKey("msg-race")]
	if !ok {
		t.Fatalf("dedup row must still exist after race; got %+v", queries.dedup)
	}
	if row.rotations < 2 {
		t.Fatalf("worker B's reclaim must have rotated the token; rotations=%d", row.rotations)
	}
	if row.token == originalToken {
		t.Fatalf("token must have rotated away from worker A's original; both=%v", originalToken)
	}

	// And the loser's audit row records duplicate (not double-ingest).
	if chat.calledAppend != 1 {
		t.Fatalf("worker A's append must have been attempted exactly once; calledAppend=%d", chat.calledAppend)
	}

	// A subsequent replay of the same message_id must still
	// duplicate-drop — the row is in the in-flight state belonging to
	// worker B's (uncompleted) run; the dispatcher cannot double-write
	// even if B's process never finishes. We simulate that by leaving
	// dedupReclaim=false so the row is treated as fresh in-flight.
	chat.beforeAppend = nil
	res2, err := d.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "third-arrival replay",
		MessageID:    "msg-race",
	})
	if err != nil {
		t.Fatalf("post-race replay should not error, got %v", err)
	}
	if res2.Outcome != OutcomeDropped || res2.DropReason != DropReasonDuplicate {
		t.Fatalf("post-race replay must duplicate-drop; got %+v", res2)
	}
}

// TestDispatcher_InTxMarkPreventsPostCommitReclaim is the regression
// for Elon's second must-fix on PR #3277 dedup: in the previous
// design, a process that committed chat_message but crashed or failed
// before MarkLarkInboundDedupProcessed left the dedup row in-flight;
// 60 seconds later a retry would treat the row as stale, re-claim it,
// and write a second chat_message. The fix moves Mark INSIDE the
// chat_message+session transaction, so the durable write and the Mark
// commit (or roll back) atomically — there is no window where
// chat_message is committed but dedup is not.
//
// This test pins the invariant by simulating a successful in-tx Mark,
// then "crashing" (Handle returns without further bookkeeping), then
// replaying the same message_id and verifying it is duplicate-dropped
// regardless of the staleness TTL setting.
func TestDispatcher_InTxMarkPreventsPostCommitReclaim(t *testing.T) {
	sessionID := validUUID(0x66)
	queries := &fakeQueries{
		installationByApp: activeInstallation(),
		userBinding:       boundUser(),
		chatSession:       db.ChatSession{ID: sessionID, AgentID: validUUID(0x33)},
	}
	chat := &fakeChat{ensureID: sessionID, appendResult: AppendResult{}}
	chat.queries = queries
	d := &Dispatcher{
		Queries:         queries,
		Chat:            chat,
		RecordDrop:      (&fakeAudit{}).RecordDrop,
		EnqueueChatTask: (&fakeEnqueuer{task: db.AgentTaskQueue{ID: validUUID(0x77)}}).EnqueueChatTask,
	}

	res, err := d.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "first delivery",
		MessageID:    "msg-atomic",
	})
	if err != nil {
		t.Fatalf("first delivery should succeed, got %v", err)
	}
	if res.Outcome != OutcomeIngested {
		t.Fatalf("first delivery must ingest; got %+v", res)
	}

	// AppendUserMessage's in-tx Mark is the sole writer.
	if queries.calledMark != 1 {
		t.Fatalf("expected exactly one Mark call (in-tx only, no post-finalize duplicate); calledMark=%d",
			queries.calledMark)
	}
	row, ok := queries.dedup[seedDedupKey("msg-atomic")]
	if !ok || !row.processed {
		t.Fatalf("dedup row must be terminal after in-tx Mark; got %+v", queries.dedup)
	}

	// Now simulate the dangerous scenario the OLD design failed: a
	// retry replays the same message_id AFTER the staleness TTL would
	// have expired. With the new design, processed_at IS NOT NULL
	// shadows the staleness check, so even with dedupReclaim=true the
	// Claim cannot re-acquire the row. The retry is a duplicate-drop.
	queries.dedupReclaim = true
	chat.calledAppend = 0
	res2, err := d.Handle(context.Background(), InboundMessage{
		AppID:        "ok",
		ChatType:     ChatTypeP2P,
		SenderOpenID: "ou_user_a",
		Body:         "after-crash retry",
		MessageID:    "msg-atomic",
	})
	if err != nil {
		t.Fatalf("post-mark retry should not error, got %v", err)
	}
	if res2.Outcome != OutcomeDropped || res2.DropReason != DropReasonDuplicate {
		t.Fatalf("post-mark retry must duplicate-drop; got %+v", res2)
	}
	if chat.calledAppend != 0 {
		t.Fatalf("post-mark retry must short-circuit before AppendUserMessage; calledAppend=%d",
			chat.calledAppend)
	}
	// The dedup row must remain processed=true and unrotated — the
	// Claim hit the terminal-row branch (no UPDATE), so claim_token
	// did NOT mint a new value.
	if row, ok := queries.dedup[seedDedupKey("msg-atomic")]; !ok || !row.processed {
		t.Fatalf("dedup row must stay terminal after replay; got %+v", queries.dedup)
	}
	if queries.dedup[seedDedupKey("msg-atomic")].rotations != 1 {
		t.Fatalf("claim_token must not rotate when the row is already processed; rotations=%d",
			queries.dedup[seedDedupKey("msg-atomic")].rotations)
	}
}
