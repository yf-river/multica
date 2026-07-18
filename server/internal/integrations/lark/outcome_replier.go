package lark

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// LarkOutcomeReplier posts the production reply for a dispatch outcome. It composes:
//
//   - APIClient — to send the binding prompt card (open_id-targeted)
//     and the offline/archived notice cards (chat_id-targeted).
//   - BindingTokenService — to mint a one-shot binding token for the
//     NeedsBinding flow.
//   - InstallationService.Credentials — to resolve the current transport identity;
//     plaintext secrets never live on the in-memory installation row.
//   - db.Queries.GetAgent — for the agent name shown on cards.
//
// The replier is constructed once at boot and shared across the Hub's
// supervisor goroutines; all dependencies must be goroutine-safe
// (the standard implementations are).
type LarkOutcomeReplier struct {
	client             APIClient
	bindingSvc         *BindingTokenService
	resolveCredentials func(db.LarkInstallation) (InstallationCredentials, error)
	getAgent           func(context.Context, pgtype.UUID) (db.Agent, error)
	publicURL          string // e.g. https://multica.example, trailing slash trimmed
	bindingPath        string // path component of the binding URL, default "/lark/bind"
	noticeHeader       string // header text used by the offline/archived cards
	log                *slog.Logger
}

// OutcomeReplierConfig wires the production replier. PublicURL is the
// Multica HTTP host the user clicks into to redeem the binding token
// (e.g. https://multica.example); empty means the binding flow can
// only log the open_id, not produce a clickable card. The other
// fields default at construction.
type OutcomeReplierConfig struct {
	APIClient          APIClient
	BindingSvc         *BindingTokenService
	ResolveCredentials func(db.LarkInstallation) (InstallationCredentials, error)
	GetAgent           func(context.Context, pgtype.UUID) (db.Agent, error)
	PublicURL          string
}

// NewLarkOutcomeReplier returns the production replier. The enabled Lark
// startup path owns dependency construction; disabled deployments never build
// a Hub or replier.
func NewLarkOutcomeReplier(cfg OutcomeReplierConfig) *LarkOutcomeReplier {
	log := slog.Default()
	if cfg.PublicURL == "" {
		log.Warn("lark outcome replier: MULTICA_PUBLIC_URL not set; binding prompt CTA will not work")
	}
	return &LarkOutcomeReplier{
		client:             cfg.APIClient,
		bindingSvc:         cfg.BindingSvc,
		resolveCredentials: cfg.ResolveCredentials,
		getAgent:           cfg.GetAgent,
		publicURL:          strings.TrimRight(cfg.PublicURL, "/"),
		bindingPath:        "/lark/bind",
		noticeHeader:       "Multica",
		log:                log,
	}
}

// Reply is the source of truth for which outcomes generate a reply; a
// missing branch silently drops the user-visible side effect.
func (r *LarkOutcomeReplier) Reply(ctx context.Context, inst db.LarkInstallation, msg InboundMessage, res DispatchResult) {
	switch res.Outcome {
	case OutcomeNeedsBinding:
		if err := r.sendBindingPrompt(ctx, inst, res); err != nil {
			r.log.Warn("lark outcome replier: binding prompt failed",
				"installation_id", util.UUIDToString(inst.ID),
				"open_id", string(res.SenderOpenID),
				"err", err.Error(),
			)
		}
	case OutcomeAgentArchived:
		if err := r.sendChatNotice(ctx, inst, msg, agentArchivedCopy); err != nil {
			r.log.Warn("lark outcome replier: archived notice failed",
				"installation_id", util.UUIDToString(inst.ID),
				"chat_id", string(msg.ChatID),
				"err", err.Error(),
			)
		}
	case OutcomeIngested:
		// The agent's chat reply itself goes through the Patcher (text
		// message on ChatDone). But /issue does NOT block on the
		// agent — the user expects an immediate "Created [MUL-42]"
		// confirmation as soon as the issue row commits, separate
		// from whatever the agent eventually replies. Without this,
		// the user types `/issue fix login bug` and just sees the
		// agent's eventual response, with no clear signal that the
		// command itself was understood. Gate on IssueID.Valid so a
		// plain chat message (no /issue) stays silent here.
		if res.IssueID.Valid {
			if err := r.sendIssueCreated(ctx, inst, msg, res); err != nil {
				r.log.Warn("lark outcome replier: issue-created confirmation failed",
					"installation_id", util.UUIDToString(inst.ID),
					"chat_id", string(msg.ChatID),
					"issue_id", util.UUIDToString(res.IssueID),
					"err", err.Error(),
				)
			}
		}
	case OutcomeDropped:
		// OutcomeDropped is informational; no user-visible reply.
	}
}

func (r *LarkOutcomeReplier) sendBindingPrompt(ctx context.Context, inst db.LarkInstallation, res DispatchResult) error {
	if res.SenderOpenID == "" {
		return errors.New("missing sender open_id")
	}
	if r.publicURL == "" {
		return errors.New("public_url not configured")
	}
	token, err := r.bindingSvc.Mint(ctx, inst.WorkspaceID, inst.ID, res.SenderOpenID)
	if err != nil {
		return fmt.Errorf("mint binding token: %w", err)
	}
	bindURL := r.publicURL + r.bindingPath + "?token=" + url.QueryEscape(token.Raw)
	creds, err := r.resolveCredentials(inst)
	if err != nil {
		return err
	}
	return r.client.SendBindingPromptCard(ctx, BindingPromptParams{
		InstallationID: creds,
		OpenID:         res.SenderOpenID,
		BindURL:        bindURL,
	})
}

// sendIssueCreated posts the "Created [MUL-42] <title>" confirmation
// as a plain text message. We deliberately send text rather than an
// interactive card so the confirmation flows inline with the rest of
// the Lark conversation — consistent with how chat replies render
// after MUL-2671's plain-text refactor. The link to Multica is
// included on its own line so Lark's auto-linker turns it into a
// tappable URL.
func (r *LarkOutcomeReplier) sendIssueCreated(ctx context.Context, inst db.LarkInstallation, msg InboundMessage, res DispatchResult) error {
	if msg.ChatID == "" {
		return errors.New("missing chat_id")
	}
	creds, err := r.resolveCredentials(inst)
	if err != nil {
		return err
	}
	text := issueCreatedText(res, r.publicURL)
	if _, err := r.client.SendTextMessage(ctx, SendTextParams{
		InstallationID: creds,
		ChatID:         msg.ChatID,
		Text:           text,
	}); err != nil {
		return fmt.Errorf("send issue-created text: %w", err)
	}
	return nil
}

// issueCreatedText composes the user-facing confirmation. Identifier
// always wins over a bare number — DispatchResult.IssueIdentifier
// already encodes the workspace prefix when available. PublicURL is
// optional: when empty (self-host operators who haven't configured
// MULTICA_PUBLIC_URL) the message still confirms the issue, just
// without a deep link the user can tap.
func issueCreatedText(res DispatchResult, publicURL string) string {
	identifier := res.IssueIdentifier
	if identifier == "" {
		identifier = fmt.Sprintf("#%d", res.IssueNumber)
	}
	title := strings.TrimSpace(res.IssueTitle)
	var line string
	if title == "" {
		line = fmt.Sprintf("Created %s", identifier)
	} else {
		line = fmt.Sprintf("Created %s — %s", identifier, title)
	}
	if publicURL == "" {
		return line
	}
	return line + "\n" + strings.TrimRight(publicURL, "/") + "/issues/" + identifier
}

func (r *LarkOutcomeReplier) sendChatNotice(ctx context.Context, inst db.LarkInstallation, msg InboundMessage, body string) error {
	if msg.ChatID == "" {
		return errors.New("missing chat_id")
	}
	creds, err := r.resolveCredentials(inst)
	if err != nil {
		return err
	}
	header := r.noticeHeader
	if agent, aerr := r.getAgent(ctx, inst.AgentID); aerr == nil && agent.Name != "" {
		header = agent.Name
	}
	cardJSON, err := renderTextCard("grey", header, body)
	if err != nil {
		return fmt.Errorf("render notice card: %w", err)
	}
	if _, err := r.client.SendInteractiveCard(ctx, SendCardParams{
		InstallationID: creds,
		ChatID:         msg.ChatID,
		CardJSON:       cardJSON,
	}); err != nil {
		return fmt.Errorf("send notice card: %w", err)
	}
	return nil
}

// agentArchivedCopy is the user-visible notice for the current archived-agent
// outcome shared by every Lark transport.
const agentArchivedCopy = "这个 Agent 已被归档，无法继续处理消息。请联系工作区管理员恢复或重新绑定。"
