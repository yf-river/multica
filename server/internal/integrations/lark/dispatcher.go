package lark

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// InboundMessage is the normalized shape the WebSocket adapter hands
// to the Dispatcher. The adapter (Phase 2 PR) translates the raw Lark
// event payload into this struct; the Dispatcher does NOT know what a
// Lark event JSON object looks like. This keeps event-schema changes
// from rippling into business logic.
//
// AddressedToBot is the adapter's verdict on whether a group-chat
// message is an interaction with the Bot (@-mention or reply to a
// Bot card). For p2p messages this field is ignored.
type InboundMessage struct {
	EventType      string
	EventID        string
	AppID          string
	ChatID         ChatID
	ChatType       ChatType
	MessageID      string
	SenderOpenID   OpenID
	Body           string
	AddressedToBot bool

	// MessageType is the raw Lark msg_type ("text", "post",
	// "merge_forward", "image", "interactive", …). The decoder
	// populates it so the inbound enricher can decide whether a
	// message needs an HTTP round-trip to expand (merge_forward) while
	// the dispatcher itself stays msg_type-agnostic and only reads Body.
	MessageType string

	// CreateTime is the trigger message's creation time (epoch
	// milliseconds, as Lark sends it). The enricher uses it to anchor the
	// group recent-context window to the moment of the @-mention — it
	// fetches the conversation up to this time rather than whatever is
	// newest when the (slightly later) prefetch HTTP call runs.
	CreateTime string

	// ParentID is the message_id of the message this one quote-replies
	// to, taken verbatim from the receive event's `parent_id`. Empty
	// when the message is not a reply. The enricher fetches it and
	// prepends a <quoted_message> block.
	ParentID string

	// CommandBody is the user's OWN typed text (the decoded Body before
	// the enricher prepends any <quoted_message> / <forwarded_messages>
	// context). The `/issue` command is parsed from THIS, not from the
	// enriched Body: enrichment prepends context blocks, which would
	// otherwise push the user's `/issue …` off the first line and
	// silently stop creating the issue. The enricher leaves CommandBody
	// untouched while it rewrites Body.
	CommandBody string
}

// Outcome categorizes what the Dispatcher decided to do with an
// inbound message. The WS adapter inspects this and chooses what to
// reply with on the Lark side.
type Outcome string

const (
	// OutcomeDropped — the message was not ingested (identity failed,
	// dedup hit, group filter, etc.). DispatchResult.DropReason holds
	// the audit category.
	OutcomeDropped Outcome = "dropped"

	// OutcomeNeedsBinding — the open_id is unbound; the WS adapter
	// should mint a binding token via BindingTokenService and send
	// the "click here to bind" card. DispatchResult.SenderOpenID is
	// populated so the adapter can target the reply.
	OutcomeNeedsBinding Outcome = "needs_binding"

	// OutcomeIngested — the message landed in chat_session and an
	// agent task was enqueued. Empty IssueCommand means a plain chat
	// message; non-empty means /issue ran (see IssueID for the new
	// issue's UUID).
	OutcomeIngested Outcome = "ingested"

	// OutcomeAgentArchived — the message landed in chat_session, but
	// the agent has been archived. The adapter should reply with a
	// distinct copy ("this agent has been archived; ask an admin to
	// unarchive or rebind").
	OutcomeAgentArchived Outcome = "agent_archived"
)

// DispatchResult is the typed return from Dispatcher.Handle. Callers
// (the WS adapter) consume this to drive their outbound side; nothing
// here implies the adapter MUST reply, only that it CAN.
type DispatchResult struct {
	Outcome       Outcome
	DropReason    DropReason
	ChatSessionID pgtype.UUID
	SenderOpenID  OpenID
	IssueID       pgtype.UUID
	IssueNumber   int32
	// IssueIdentifier is the workspace-qualified human key for the
	// created issue ("MUL-42"). Populated only when /issue produced a
	// new row. The OutcomeReplier uses this verbatim in the "Created
	// [MUL-42]" confirmation message.
	IssueIdentifier string
	// IssueTitle is the title the user supplied on /issue, echoed back
	// in the confirmation message so the chat history reads naturally
	// even when the Multica deep link is not reachable.
	IssueTitle string
}

// DispatcherQueries is the narrow subset of *db.Queries the Dispatcher
// needs for installation routing, identity lookup, dedup, and session
// reload. *db.Queries satisfies it directly; tests substitute a fake.
//
// Dedup is two-phase with owner fencing:
//
//   - ClaimLarkInboundDedup mints a fresh claim_token UUID on insert
//     and on stale-reclaim re-take. The token is the dispatcher's
//     ownership receipt for the row.
//
//   - MarkLarkInboundDedupProcessed and ReleaseLarkInboundDedup are
//     fenced on (message_id, claim_token, processed_at IS NULL). A
//     stale-reclaim that rotates the token invalidates earlier
//     finalizers, so a slow-but-alive worker whose claim was taken
//     over cannot stomp the new holder's row. Both queries return
//     rowsAffected; zero means "your token is no longer the live one"
//     and the dispatcher treats it as a no-op (not an error — the
//     other worker is responsible for the row now).
//
//   - AppendUserMessage invokes the Mark INSIDE its chat_message tx
//     when a claim token is supplied, so the durable write and the
//     Mark commit atomically. That closes the "crashed between
//     commit and Mark" window. See the lark_inbound_message_dedup
//     schema comment for the full invariant set.
type DispatcherQueries interface {
	GetLarkInstallationByAppID(ctx context.Context, appID string) (db.LarkInstallation, error)
	GetLarkUserBindingByOpenID(ctx context.Context, arg db.GetLarkUserBindingByOpenIDParams) (db.LarkUserBinding, error)
	GetChatSession(ctx context.Context, id pgtype.UUID) (db.ChatSession, error)
	ClaimLarkInboundDedup(ctx context.Context, arg db.ClaimLarkInboundDedupParams) (db.LarkInboundMessageDedup, error)
	MarkLarkInboundDedupProcessed(ctx context.Context, arg db.MarkLarkInboundDedupProcessedParams) (int64, error)
	ReleaseLarkInboundDedup(ctx context.Context, arg db.ReleaseLarkInboundDedupParams) (int64, error)
	// GetWorkspace is needed to read IssuePrefix so the /issue
	// confirmation message can render the workspace-qualified key
	// ("MUL-42"). A lookup failure is non-fatal — we degrade to
	// emitting just the issue number — so callers handle the error
	// inline rather than aborting the whole dispatch.
	GetWorkspace(ctx context.Context, id pgtype.UUID) (db.Workspace, error)
}

// Dispatcher is the single per-message entry point on the inbound
// path. It owns the order in which identity check, group filter,
// dedup, ingest, /issue, and task enqueue happen — the WS adapter
// MUST NOT bypass it. That ordering is the invariant that keeps the
// design's §4.3 safety property ("unbound users never reach
// chat_session") true at runtime.
type Dispatcher struct {
	Queries         DispatcherQueries
	Chat            ChatSessionService
	RecordDrop      func(context.Context, AuditDropParams) error
	CreateIssue     func(context.Context, service.IssueCreateParams, service.IssueCreateOpts) (service.IssueCreateResult, error)
	EnqueueChatTask func(context.Context, db.ChatSession, pgtype.UUID) (db.AgentTaskQueue, error)

	// FlushReply emits the offline/archived notice that EnqueueChatTask
	// now produces only at debounce-flush time. Before MUL-2968 those
	// outcomes were returned synchronously from Handle and the hub's
	// OutcomeReplier sent the card; with the run trigger debounced, the
	// verdict is not known until the window closes, so the dispatcher
	// drives the reply itself via this callback. Wired to
	// LarkOutcomeReplier.Reply in production.
	FlushReply ReplyFunc

	// Logger is used by the detached flush path, which cannot return
	// errors to a caller and must log them. Defaults to slog.Default().
	Logger *slog.Logger

	// batcher debounces the per-session run trigger. Its zero value is the
	// production configuration, with the default silence window and real timer.
	batcher pendingBatcher
}

// ReplyFunc is the outbound half of the EventEmitter contract. The Hub and
// the debounced dispatcher flush both invoke the production replier through
// this exact callback instead of maintaining parallel one-method interfaces.
type ReplyFunc func(ctx context.Context, inst db.LarkInstallation, msg InboundMessage, res DispatchResult)

// chatRunFlushTimeout bounds the detached flush (session reload +
// EnqueueChatTask + offline/archived notice). The flush runs on its own
// fresh context because the inbound request ctx is long cancelled by the
// time the window closes.
const chatRunFlushTimeout = 10 * time.Second

// FlushPendingRuns drains every still-pending run trigger immediately and
// blocks until in-flight flushes finish. The hub calls this on graceful
// shutdown, after inbound delivery has stopped, so a normal restart does
// not silently drop a window's worth of messages.
func (d *Dispatcher) FlushPendingRuns() {
	d.batcher.FlushAll()
}

func (d *Dispatcher) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// Handle processes one inbound Lark message end-to-end. It never
// returns an error for "this message was dropped" — those are
// reported via Outcome + DropReason and a non-nil err is reserved for
// real infrastructure failures (DB down, etc.) that the WS adapter
// should retry.
//
// Dedup is two-phase. After the installation lookup, ClaimLarkInbound-
// Dedup acquires an in-flight claim on msg.MessageID. After the rest
// of the pipeline runs, the claim is finalized exactly once:
//
//   - MarkLarkInboundDedupProcessed — a durable outcome was reached
//     (audit drop row persisted, OR chat_message + session touched).
//     Future replays of this message_id are dropped as duplicates.
//
//   - ReleaseLarkInboundDedup — an infra error occurred BEFORE any
//     durable side effect. The claim row is deleted so the WS
//     adapter's retry can re-acquire it immediately; otherwise the
//     message would be permanently swallowed as a duplicate even
//     though it never actually landed in chat_session.
func (d *Dispatcher) Handle(ctx context.Context, msg InboundMessage) (DispatchResult, error) {
	// 1. Route to installation. The app_id is the only identifier
	//    that ties an event to its installation row. These two drop
	//    branches run BEFORE the dedup claim because they have no
	//    valid installation row to attach to — see the spec note on
	//    lark_inbound_audit allowing a NULL installation_id.
	inst, err := d.Queries.GetLarkInstallationByAppID(ctx, msg.AppID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := d.RecordDrop(ctx, AuditDropParams{
				EventType:     msg.EventType,
				LarkEventID:   msg.EventID,
				LarkMessageID: msg.MessageID,
				ChatID:        msg.ChatID,
				Reason:        DropReasonInvalidEvent,
			}); err != nil {
				return DispatchResult{}, fmt.Errorf("record invalid-event drop: %w", err)
			}
			return DispatchResult{Outcome: OutcomeDropped, DropReason: DropReasonInvalidEvent}, nil
		}
		return DispatchResult{}, fmt.Errorf("load installation: %w", err)
	}
	if inst.Status != installationStatusActive {
		return d.drop(ctx, msg, inst.ID, DropReasonRevokedInstallation)
	}

	// 2. Two-phase dedup claim with owner fencing. Spec §4.3 puts this
	//    before group filter and identity check so a WebSocket
	//    reconnect that replays an event cannot:
	//      a) re-trigger the binding prompt for an unbound user, or
	//      b) re-write the not_addressed_in_group / unbound_user audit
	//         rows, or
	//      c) re-touch the chat_session for a bound message.
	//
	//    The Claim returns claim_token; subsequent Mark / Release calls
	//    are fenced on (message_id, claim_token), and AppendUserMessage
	//    invokes the Mark INSIDE its chat_message tx, so the durable
	//    write + Mark commit atomically. Stale-reclaim by another
	//    worker rotates the token, which invalidates our same-tx Mark
	//    (zero rows → ErrClaimLost → tx rollback).
	//
	//    Empty MessageID means the event has no Lark message_id at all
	//    (non-message events, malformed payloads); skipping dedup is
	//    the safe default — we have no key to deduplicate by, and no
	//    claim to finalize at the end.
	var claimToken pgtype.UUID
	claimed := false
	if msg.MessageID != "" {
		claim, err := d.Queries.ClaimLarkInboundDedup(ctx, db.ClaimLarkInboundDedupParams{
			InstallationID: inst.ID,
			MessageID:      msg.MessageID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Either the row is processed_at IS NOT NULL
				// (terminal) or another worker is actively
				// processing. Either way, the right behavior is to
				// drop without re-doing the work.
				return d.drop(ctx, msg, inst.ID, DropReasonDuplicate)
			}
			return DispatchResult{}, fmt.Errorf("dedup claim: %w", err)
		}
		claimToken = claim.ClaimToken
		claimed = true
	}

	res, finalize, err := d.processClaimed(ctx, msg, inst, claimToken)

	if claimed {
		d.applyFinalize(ctx, inst.ID, msg.MessageID, claimToken, finalize)
	}

	// ErrClaimLost is the dispatcher's signal that another worker
	// holds the claim. Surface it as a duplicate drop to the caller —
	// nothing else needs to happen, and the audit row was already
	// written by the in-tx rollback path's caller (see processClaimed).
	if errors.Is(err, ErrClaimLost) {
		return d.drop(ctx, msg, inst.ID, DropReasonDuplicate)
	}

	return res, err
}

// dedupFinalize captures the dispatcher's instruction to applyFinalize
// after processClaimed returns. The three states correspond to the
// three terminal positions in the inbound pipeline:
//
//   - finalizeMark: a durable side effect landed OUTSIDE
//     AppendUserMessage's tx (audit drop row, or a post-AppendUser-
//     Message error that left the chat_message committed). Token-
//     fenced Mark locks the row terminal.
//
//   - finalizeRelease: the run did not reach durability. Delete the
//     in-flight row so the WS adapter's retry can re-claim it
//     immediately instead of waiting for the 60s staleness TTL.
//
//   - finalizeNone: AppendUserMessage already finalized the row in
//     its own tx (success → Mark in-tx; ErrClaimLost → another worker
//     owns it). The dispatcher does not touch the row again.
type dedupFinalize int

const (
	finalizeNone dedupFinalize = iota
	finalizeMark
	finalizeRelease
)

// processClaimed runs the post-dedup pipeline. It returns the typed
// dispatch result, a dedupFinalize directive telling the caller how to
// land the claim row, and any error.
//
// Boundary contract per step:
//
//   - Group filter / unbound-user drop → audit row written →
//     finalizeMark.
//   - EnsureChatSession error → tx rolled back, no durable side effect
//     → finalizeRelease.
//   - AppendUserMessage success → chat_message committed AND
//     dedup row already Marked in the same tx → finalizeNone.
//   - AppendUserMessage error → tx rolled back, no chat_message →
//     finalizeRelease (ErrClaimLost is treated specially by Handle).
//   - Post-AppendUserMessage error (issue create, session reload,
//     task enqueue) → chat_message already committed but the
//     in-tx Mark also already committed → finalizeNone.
func (d *Dispatcher) processClaimed(ctx context.Context, msg InboundMessage, inst db.LarkInstallation, claimToken pgtype.UUID) (DispatchResult, dedupFinalize, error) {
	// 3. Group-mention filter (group chats only). We do this BEFORE
	//    identity check so that an unbound user's idle group chatter
	//    never produces an "you need to bind" reply card spam — the
	//    Bot is not addressed, so we say nothing.
	if msg.ChatType == ChatTypeGroup && !msg.AddressedToBot {
		res, err := d.drop(ctx, msg, inst.ID, DropReasonNotAddressedInGroup)
		if err != nil {
			return DispatchResult{}, finalizeRelease, err
		}
		return res, finalizeMark, nil
	}

	// 4. Identity check. A row in lark_user_binding means the open_id
	//    maps to a current workspace member (the composite FK to
	//    member cascades the binding away on membership revocation).
	binding, err := d.Queries.GetLarkUserBindingByOpenID(ctx, db.GetLarkUserBindingByOpenIDParams{
		InstallationID: inst.ID,
		LarkOpenID:     string(msg.SenderOpenID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := d.RecordDrop(ctx, AuditDropParams{
				InstallationID: inst.ID,
				ChatID:         msg.ChatID,
				EventType:      msg.EventType,
				LarkEventID:    msg.EventID,
				LarkMessageID:  msg.MessageID,
				Reason:         DropReasonUnboundUser,
			}); err != nil {
				return DispatchResult{}, finalizeRelease, fmt.Errorf("record unbound-user drop: %w", err)
			}
			return DispatchResult{
				Outcome:      OutcomeNeedsBinding,
				DropReason:   DropReasonUnboundUser,
				SenderOpenID: msg.SenderOpenID,
			}, finalizeMark, nil
		}
		return DispatchResult{}, finalizeRelease, fmt.Errorf("load user binding: %w", err)
	}

	// 5. Resolve the chat_session. For group chats, the session
	//    creator is the INSTALLER (stable workspace identity that
	//    won't cascade-delete when individual group members churn);
	//    for p2p, the sender is the one and only human in the chat
	//    so we use them.
	sessionCreator := binding.MulticaUserID
	if msg.ChatType == ChatTypeGroup {
		sessionCreator = inst.InstallerUserID
	}
	sessionID, err := d.Chat.EnsureChatSession(ctx, EnsureChatSessionParams{
		WorkspaceID:    inst.WorkspaceID,
		InstallationID: inst.ID,
		AgentID:        inst.AgentID,
		ChatID:         msg.ChatID,
		ChatType:       msg.ChatType,
		Sender:         sessionCreator,
	})
	if err != nil {
		// chat_session create + lark_chat_session_binding create are
		// in a single tx; an error here means the tx rolled back and
		// nothing landed. Safe to release the dedup claim.
		return DispatchResult{}, finalizeRelease, fmt.Errorf("ensure chat session: %w", err)
	}

	// 6. Append message + in-tx dedup Mark — the durable transition
	//    point. After this returns nil the chat_message AND the dedup
	//    Mark have committed atomically; any subsequent failure path
	//    must return finalizeNone (the row is already terminal,
	//    re-Marking is a no-op but re-Releasing would undo nothing
	//    and we don't want to call DELETE on a Marked row).
	//
	//    ErrClaimLost = our token was rotated by a stale-reclaim mid-
	//    flight; the deferred Rollback in AppendUserMessage already
	//    undid the chat_message insert, so Handle treats this as a
	//    duplicate drop. finalizeNone — the other holder owns the row.
	appendRes, err := d.Chat.AppendUserMessage(ctx, AppendUserMessageParams{
		ChatSessionID:  sessionID,
		Sender:         binding.MulticaUserID,
		Body:           msg.Body,
		CommandBody:    msg.CommandBody,
		InstallationID: inst.ID,
		LarkMessageID:  msg.MessageID,
		ClaimToken:     claimToken,
	})
	if err != nil {
		if errors.Is(err, ErrClaimLost) {
			return DispatchResult{}, finalizeNone, err
		}
		// AppendUserMessage's transaction either commits or rolls
		// back atomically; an error means rollback, so no
		// chat_message was written. Safe to release.
		return DispatchResult{}, finalizeRelease, fmt.Errorf("append user message: %w", err)
	}

	res := DispatchResult{
		Outcome:       OutcomeIngested,
		ChatSessionID: sessionID,
	}

	// 7. /issue command, if present. chat_message is already durable
	//    above; from here all error returns must signal finalizeNone.
	if appendRes.IssueCommand != nil {
		issueRes, err := d.createIssueFromCommand(ctx, inst, binding.MulticaUserID, sessionID, *appendRes.IssueCommand)
		if err != nil {
			return DispatchResult{}, finalizeNone, fmt.Errorf("create issue from command: %w", err)
		}
		res.IssueID = issueRes.Issue.ID
		res.IssueNumber = issueRes.Issue.Number
		res.IssueTitle = issueRes.Issue.Title
		// Render the workspace-qualified key ("MUL-42") so the
		// outbound confirmation reads like a Linear/Jira identifier
		// rather than a bare number. A workspace lookup failure here
		// degrades gracefully — we still surface the issue number,
		// just without the workspace prefix — so a Postgres blip on
		// the workspace row does not eat the "/issue created" signal.
		if ws, werr := d.Queries.GetWorkspace(ctx, inst.WorkspaceID); werr == nil && ws.IssuePrefix != "" {
			res.IssueIdentifier = fmt.Sprintf("%s-%d", ws.IssuePrefix, issueRes.Issue.Number)
		} else {
			res.IssueIdentifier = fmt.Sprintf("#%d", issueRes.Issue.Number)
		}
	}

	// 8. Debounce the run trigger. The chat_message + dedup Mark are
	//    already durable; the agent run reads the WHOLE session at
	//    execution time, so a burst of messages in this session is
	//    collapsed into ONE run by deferring EnqueueChatTask behind a
	//    short silence window (MUL-2968). The task row is created later,
	//    at flush. EnqueueChatTask's productizable verdicts (agent
	//    offline / archived) and infra errors are now handled inside the
	//    flush (see flushChatRun), not returned here.
	//
	//    Note: a daemon that's merely disconnected is NOT an error. As
	//    long as agent.runtime_id is set, the chat task is enqueued at
	//    flush and waits for the daemon to claim it on next online.
	// binding.MulticaUserID is THIS message's sender — the task initiator. It is
	// deliberately not the session creator (group sessions are creator=installer,
	// see step 5). The debouncer keeps the latest scheduled flush per session, so
	// in a multi-sender silence window the last sender wins, matching the
	// "latest message in a window wins" rule above. See MUL-2645.
	d.scheduleRun(inst, msg, sessionID, binding.MulticaUserID)
	return res, finalizeNone, nil
}

// scheduleRun hands the per-session run trigger to the debouncer. The flush
// closure captures this message's installation + InboundMessage so an archived
// notice targets the right chat; the latest message in a window wins.
func (d *Dispatcher) scheduleRun(inst db.LarkInstallation, msg InboundMessage, sessionID pgtype.UUID, initiatorUserID pgtype.UUID) {
	flush := func() { d.flushChatRun(inst, msg, sessionID, initiatorUserID) }
	d.batcher.Schedule(string(sessionID.Bytes[:]), flush)
}

// flushChatRun is the debounced run-trigger. It runs once per silence
// window per chat session, detached from the inbound path (on its own
// goroutine and fresh context). It reloads the session, enqueues exactly
// one chat task for the whole window's worth of messages, and — because
// EnqueueChatTask's archived verdict is only known here now — emits the
// corresponding notice itself via FlushReply. Errors cannot be
// returned to a caller (the message is already ACKed and durable), so they
// are logged: a failed enqueue leaves the message in the session to be
// picked up by the next message's run.
func (d *Dispatcher) flushChatRun(inst db.LarkInstallation, msg InboundMessage, sessionID pgtype.UUID, initiatorUserID pgtype.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), chatRunFlushTimeout)
	defer cancel()

	session, err := d.Queries.GetChatSession(ctx, sessionID)
	if err != nil {
		d.logger().Error("lark dispatcher: flush reload chat session failed",
			"chat_session_id", util.UUIDToString(sessionID),
			"err", err.Error(),
		)
		return
	}
	if _, err := d.EnqueueChatTask(ctx, session, initiatorUserID); err != nil {
		switch {
		case errors.Is(err, service.ErrChatTaskAgentArchived):
			d.emitFlushReply(ctx, inst, msg, OutcomeAgentArchived)
		default:
			// Infra failure (DB down, etc.). Nothing to retry against —
			// the inbound frame was ACKed long ago. Log so the gap is
			// visible; the next message in this session re-triggers a run
			// that will read this message too.
			d.logger().Error("lark dispatcher: flush enqueue chat task failed",
				"chat_session_id", util.UUIDToString(sessionID),
				"err", err.Error(),
			)
		}
	}
}

// emitFlushReply delivers an offline/archived notice for a flushed run.
func (d *Dispatcher) emitFlushReply(ctx context.Context, inst db.LarkInstallation, msg InboundMessage, outcome Outcome) {
	d.FlushReply(ctx, inst, msg, DispatchResult{
		Outcome: outcome,
	})
}

// applyFinalize flips the in-flight claim row to its terminal state,
// token-fenced so a slow-but-alive worker whose claim was reclaimed
// cannot stomp the new holder's row.
//
// Best-effort by design for the I/O layer: a transport failure here
// cannot abort the outcome (the user's message is already in
// chat_session or the audit row already exists), and the worst case
// is a stuck in-flight row that the 60-second staleness fallback in
// ClaimLarkInboundDedup re-takes on retry. zero-rows-affected is the
// EXPECTED outcome whenever our token was rotated; it is not an error.
func (d *Dispatcher) applyFinalize(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID, action dedupFinalize) {
	switch action {
	case finalizeMark:
		rows, err := d.Queries.MarkLarkInboundDedupProcessed(ctx, db.MarkLarkInboundDedupProcessedParams{
			InstallationID: installationID,
			MessageID:      messageID,
			ClaimToken:     claimToken,
		})
		if err != nil {
			d.logger().Error("lark dispatcher: finalize dedup mark failed", "installation_id", util.UUIDToString(installationID), "message_id", messageID, "err", err)
		} else if rows == 0 {
			d.logger().Warn("lark dispatcher: finalize dedup mark lost claim", "installation_id", util.UUIDToString(installationID), "message_id", messageID)
		}
	case finalizeRelease:
		rows, err := d.Queries.ReleaseLarkInboundDedup(ctx, db.ReleaseLarkInboundDedupParams{
			InstallationID: installationID,
			MessageID:      messageID,
			ClaimToken:     claimToken,
		})
		if err != nil {
			d.logger().Error("lark dispatcher: finalize dedup release failed", "installation_id", util.UUIDToString(installationID), "message_id", messageID, "err", err)
		} else if rows == 0 {
			d.logger().Warn("lark dispatcher: finalize dedup release lost claim", "installation_id", util.UUIDToString(installationID), "message_id", messageID)
		}
	case finalizeNone:
		// AppendUserMessage already finalized the row in-tx, or our
		// claim was lost to a concurrent reclaim. Do not touch it.
	}
}

func (d *Dispatcher) drop(ctx context.Context, msg InboundMessage, instID pgtype.UUID, reason DropReason) (DispatchResult, error) {
	if err := d.RecordDrop(ctx, AuditDropParams{
		InstallationID: instID,
		ChatID:         msg.ChatID,
		EventType:      msg.EventType,
		LarkEventID:    msg.EventID,
		LarkMessageID:  msg.MessageID,
		Reason:         reason,
	}); err != nil {
		return DispatchResult{}, fmt.Errorf("record %s drop: %w", reason, err)
	}
	return DispatchResult{
		Outcome:    OutcomeDropped,
		DropReason: reason,
	}, nil
}

func (d *Dispatcher) createIssueFromCommand(
	ctx context.Context,
	inst db.LarkInstallation,
	creatorUserID pgtype.UUID,
	sessionID pgtype.UUID,
	cmd IssueCommand,
) (service.IssueCreateResult, error) {
	// Empty title at this point means the /issue alone fallback found
	// no previous user message either. The product copy ("请填标题")
	// belongs in the WS adapter's reply card; we surface this to the
	// caller as ErrEmptyIssueTitle so the dispatcher can short-circuit
	// without paying the IssueService cost.
	if cmd.Title == "" {
		return service.IssueCreateResult{}, ErrEmptyIssueTitle
	}
	params := service.IssueCreateParams{
		WorkspaceID:  inst.WorkspaceID,
		Title:        cmd.Title,
		Description:  pgtype.Text{String: cmd.Description, Valid: cmd.Description != ""},
		Status:       "todo",
		Priority:     "none",
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   inst.AgentID,
		CreatorType:  "member",
		CreatorID:    creatorUserID,
		OriginType:   pgtype.Text{String: originLarkChat, Valid: true},
		OriginID:     sessionID,
	}
	return d.CreateIssue(ctx, params, service.IssueCreateOpts{})
}

// originLarkChat is the issue.origin_type label written for issues
// created via the Lark `/issue` command. The analytics classifier in
// service.classifyOrigin currently maps unknown origin_type values to
// SourceManual with a warning — that is acceptable for MVP. A
// dedicated analytics source label can be added when product asks for
// it.
const originLarkChat = "lark_chat"

// ErrEmptyIssueTitle is returned by createIssueFromCommand when the
// caller invoked /issue with no title AND the previous-user-message
// fallback found nothing usable. The WS adapter translates this into
// the "please supply a title" reply card per §2.3.
var ErrEmptyIssueTitle = errors.New("issue title is empty")
