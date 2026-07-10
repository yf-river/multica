package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) HandleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MiB cap
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body failed")
		return
	}
	secret := githubWebhookSecret()
	if secret == "" {
		// Refusing to process webhooks at all is safer than treating an
		// unconfigured deployment as "all signatures valid".
		writeError(w, http.StatusServiceUnavailable, "github webhooks not configured")
		return
	}
	sigHeader := r.Header.Get("X-Hub-Signature-256")
	if !verifyWebhookSignature(secret, sigHeader, body) {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	ctx := r.Context()
	switch event {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]string{"ok": "pong"})
		return
	case "installation":
		h.handleInstallationEvent(ctx, body)
	case "pull_request":
		h.handlePullRequestEvent(ctx, body)
	case "check_suite":
		h.handleCheckSuiteEvent(ctx, body)
	default:
		// Acknowledge every event so GitHub doesn't mark the endpoint failing,
		// but ignore types we don't model.
	}
	w.WriteHeader(http.StatusAccepted)
}

func verifyWebhookSignature(secret, header string, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

type ghInstallationPayload struct {
	Action       string `json:"action"`
	Installation struct {
		ID      int64 `json:"id"`
		Account struct {
			Login     string `json:"login"`
			Type      string `json:"type"`
			AvatarURL string `json:"avatar_url"`
		} `json:"account"`
	} `json:"installation"`
}

func githubInstallationAccountFromPayload(p ghInstallationPayload) (login, accountType string, avatar *string, ok bool) {
	login = strings.TrimSpace(p.Installation.Account.Login)
	if login == "" {
		return "", "", nil, false
	}
	accountType = coalesce(p.Installation.Account.Type, "User")
	avatar = strPtrOrNil(p.Installation.Account.AvatarURL)
	return login, accountType, avatar, true
}

func (h *Handler) handleInstallationEvent(ctx context.Context, body []byte) {
	var p ghInstallationPayload
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("github: bad installation payload", "err", err)
		return
	}
	switch p.Action {
	case "deleted", "suspend":
		// User removed the App on GitHub — drop our row so the workspace
		// stops trusting this installation_id. We DELETE … RETURNING so
		// the broadcast can be scoped to the right workspace; events
		// without WorkspaceID are dropped by the realtime listener and
		// would leave already-open Settings tabs stale.
		deleted, err := h.Queries.DeleteGitHubInstallationByInstallationID(ctx, p.Installation.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				if err := h.Queries.DeletePendingGitHubInstallation(ctx, p.Installation.ID); err != nil {
					slog.Warn("github: delete pending installation failed", "err", err, "installation_id", p.Installation.ID)
				}
				return // already gone — nothing to broadcast
			}
			slog.Warn("github: delete installation failed", "err", err, "installation_id", p.Installation.ID)
			return
		}
		if err := h.Queries.DeletePendingGitHubInstallation(ctx, p.Installation.ID); err != nil {
			slog.Warn("github: delete pending installation failed", "err", err, "installation_id", p.Installation.ID)
		}
		// Broadcast the internal row id only — the numeric installation_id is
		// a management handle that non-admin members are not allowed to see.
		// The frontend invalidates the installations query on this event and
		// does not read the broadcast payload directly.
		h.publish(protocol.EventGitHubInstallationDeleted, uuidToString(deleted.WorkspaceID), "system", "", map[string]any{
			"id": uuidToString(deleted.ID),
		})
	case "created", "new_permissions_accepted", "unsuspend":
		login, accountType, avatar, ok := githubInstallationAccountFromPayload(p)
		if !ok {
			slog.Warn("github: installation payload missing account login", "installation_id", p.Installation.ID)
			return
		}

		// We don't know which workspace this maps to from the webhook alone.
		// If the setup callback has not created the workspace binding yet,
		// keep the account metadata and let the callback consume it after it
		// creates github_installation.
		existing, err := h.Queries.GetGitHubInstallationByInstallationID(ctx, p.Installation.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				if _, err := h.Queries.UpsertPendingGitHubInstallation(ctx, db.UpsertPendingGitHubInstallationParams{
					InstallationID:   p.Installation.ID,
					AccountLogin:     login,
					AccountType:      accountType,
					AccountAvatarUrl: ptrToText(avatar),
				}); err != nil {
					slog.Warn("github: store pending installation failed", "err", err, "installation_id", p.Installation.ID)
				}
				return
			}
			slog.Warn("github: lookup installation failed", "err", err, "installation_id", p.Installation.ID)
			return
		}
		inst, err := h.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
			WorkspaceID:      existing.WorkspaceID,
			InstallationID:   p.Installation.ID,
			AccountLogin:     login,
			AccountType:      accountType,
			AccountAvatarUrl: ptrToText(avatar),
			ConnectedByID:    existing.ConnectedByID,
		})
		if err != nil {
			slog.Warn("github: refresh installation failed", "err", err)
			return
		}
		if err := h.Queries.DeletePendingGitHubInstallation(ctx, p.Installation.ID); err != nil {
			slog.Warn("github: delete pending installation failed", "err", err, "installation_id", p.Installation.ID)
		}
		// Broadcast so any open Settings → GitHub tab re-queries the
		// installations list. Without this, a row created by the setup
		// callback with the "unknown" placeholder (e.g. because GitHub
		// App JWT auth wasn't configured, or this webhook arrived after
		// the user already loaded the page) would stay visibly stale
		// until the user manually refreshes.
		h.publish(protocol.EventGitHubInstallationCreated, uuidToString(inst.WorkspaceID), "system", "", map[string]any{
			"installation": githubInstallationToBroadcast(inst),
		})
	}
}

type ghPullRequestPayload struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number         int32  `json:"number"`
		HTMLURL        string `json:"html_url"`
		Title          string `json:"title"`
		Body           string `json:"body"`
		State          string `json:"state"`
		Draft          bool   `json:"draft"`
		Merged         bool   `json:"merged"`
		MergedAt       string `json:"merged_at"`
		ClosedAt       string `json:"closed_at"`
		CreatedAt      string `json:"created_at"`
		UpdatedAt      string `json:"updated_at"`
		MergeableState string `json:"mergeable_state"`
		Additions      int32  `json:"additions"`
		Deletions      int32  `json:"deletions"`
		ChangedFiles   int32  `json:"changed_files"`
		Head           struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		User struct {
			Login     string `json:"login"`
			AvatarURL string `json:"avatar_url"`
		} `json:"user"`
	} `json:"pull_request"`
	Changes    *ghPRChanges `json:"changes"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

func (h *Handler) handlePullRequestEvent(ctx context.Context, body []byte) {
	var p ghPullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("github: bad pull_request payload", "err", err)
		return
	}
	if p.Installation.ID == 0 {
		return
	}
	inst, err := h.Queries.GetGitHubInstallationByInstallationID(ctx, p.Installation.ID)
	if err != nil {
		// Webhook from an installation we never wired up — nothing we
		// can attribute to a workspace, so drop it silently.
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("github: lookup installation failed", "err", err)
		}
		return
	}

	state := derivePRState(p.PullRequest.State, p.PullRequest.Draft, p.PullRequest.Merged)
	mergeable, clearMergeable := derivePRMergeableState(p.Action, p.PullRequest.MergeableState, baseRefChanged(p.Changes))
	pr, err := h.Queries.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
		WorkspaceID:         inst.WorkspaceID,
		InstallationID:      inst.InstallationID,
		RepoOwner:           p.Repository.Owner.Login,
		RepoName:            p.Repository.Name,
		PrNumber:            p.PullRequest.Number,
		Title:               p.PullRequest.Title,
		State:               state,
		HtmlUrl:             p.PullRequest.HTMLURL,
		Branch:              ptrToText(strPtrOrNil(p.PullRequest.Head.Ref)),
		AuthorLogin:         ptrToText(strPtrOrNil(p.PullRequest.User.Login)),
		AuthorAvatarUrl:     ptrToText(strPtrOrNil(p.PullRequest.User.AvatarURL)),
		MergedAt:            parseGHTime(p.PullRequest.MergedAt),
		ClosedAt:            parseGHTime(p.PullRequest.ClosedAt),
		PrCreatedAt:         parseGHTimeRequired(p.PullRequest.CreatedAt),
		PrUpdatedAt:         parseGHTimeRequired(p.PullRequest.UpdatedAt),
		HeadSha:             p.PullRequest.Head.SHA,
		MergeableState:      mergeable,
		ClearMergeableState: pgtype.Bool{Bool: clearMergeable, Valid: true},
		Additions:           p.PullRequest.Additions,
		Deletions:           p.PullRequest.Deletions,
		ChangedFiles:        p.PullRequest.ChangedFiles,
	})
	if err != nil {
		slog.Warn("github: upsert pr failed", "err", err)
		return
	}

	// Drain any check_suite events that arrived before this PR row was
	// mirrored (out-of-order webhook delivery). Each drained row is
	// replayed through the same upsert path used by live check_suite
	// events; the DrainPending… query removes them atomically so a
	// concurrent PR upsert can't double-apply.
	h.replayPendingCheckSuitesForPR(ctx, pr, inst.WorkspaceID)

	workspaceID := uuidToString(inst.WorkspaceID)
	resp := githubPullRequestToResponse(pr)

	// Webhooks mirror provider MR/PR state, but issue association is now a
	// synchronous platform action (`multica issue mr create` or explicit link).
	// We intentionally do not infer links from branch/title/body identifiers:
	// async webhooks are too delayed and deployment-dependent to be a task
	// completion gate.
	linkedIssueIDs := make([]string, 0)

	// Broadcast PR change to the workspace so any open issue detail page
	// re-queries its PR list.
	h.publish(protocol.EventPullRequestUpdated, workspaceID, "system", "", map[string]any{
		"pull_request":     resp,
		"linked_issue_ids": linkedIssueIDs,
	})
}

// ── check_suite webhook ────────────────────────────────────────────────────

type ghCheckSuitePayload struct {
	Action     string `json:"action"`
	CheckSuite struct {
		ID         int64  `json:"id"`
		HeadSHA    string `json:"head_sha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		UpdatedAt  string `json:"updated_at"`
		App        struct {
			ID int64 `json:"id"`
		} `json:"app"`
		PullRequests []struct {
			Number int32 `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_suite"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// handleCheckSuiteEvent records the CI suite state for each PR the suite
// references. We persist all non-terminal actions (`requested`, `rerequested`)
// as well as `completed`: a `requested`/`rerequested` event has status
// `queued`/`in_progress` and an empty conclusion, which the aggregation query
// counts as pending. Without persisting them, the per-PR `checks_pending`
// count stays at 0 while CI is mid-run and the PR card falls through to
// "checks not reported yet" until the first suite finishes.
//
// The suite payload may reference multiple PRs (e.g. the same head SHA is
// open against several base branches), so we iterate. A reference whose PR
// hasn't been mirrored locally is stashed in `github_pending_check_suite`
// and replayed when the matching `pull_request` event upserts the PR row.
func (h *Handler) handleCheckSuiteEvent(ctx context.Context, body []byte) {
	var p ghCheckSuitePayload
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("github: bad check_suite payload", "err", err)
		return
	}
	if p.Installation.ID == 0 {
		return
	}
	inst, err := h.Queries.GetGitHubInstallationByInstallationID(ctx, p.Installation.ID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("github: lookup installation failed", "err", err)
		}
		return
	}
	if len(p.CheckSuite.PullRequests) == 0 {
		// Forks emit suites whose `pull_requests` array is empty for
		// the upstream repo. We have no way to attribute the result
		// without polling, so drop with a hint.
		slog.Info("github: check_suite has no associated PRs", "suite_id", p.CheckSuite.ID)
		return
	}
	updatedAt := parseGHTimeRequired(p.CheckSuite.UpdatedAt)

	affectedWorkspaces := map[string]struct{}{}
	affectedIssues := map[string]struct{}{}
	for _, prRef := range p.CheckSuite.PullRequests {
		// Scope the lookup to the installation's workspace. The
		// (workspace_id, repo_owner, repo_name, pr_number) tuple is the
		// real uniqueness key: if the same repo lived under a different
		// workspace historically, a bare (owner, repo, number) lookup
		// could return either row arbitrarily and land this suite on
		// the wrong PR (or skip the right one because the installation
		// ids no longer match).
		pr, err := h.Queries.GetGitHubPullRequest(ctx, db.GetGitHubPullRequestParams{
			WorkspaceID: inst.WorkspaceID,
			RepoOwner:   p.Repository.Owner.Login,
			RepoName:    p.Repository.Name,
			PrNumber:    prRef.Number,
		})
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("github: lookup pr for check_suite failed", "err", err)
				continue
			}
			// Out-of-order delivery: the suite reached us before the
			// `pull_request` webhook that mirrors the PR row. Stash the
			// event keyed by (workspace, repo, pr_number, suite_id); the
			// PR upsert path will drain and replay it.
			if err := h.Queries.UpsertPendingCheckSuite(ctx, db.UpsertPendingCheckSuiteParams{
				WorkspaceID:    inst.WorkspaceID,
				InstallationID: p.Installation.ID,
				RepoOwner:      p.Repository.Owner.Login,
				RepoName:       p.Repository.Name,
				PrNumber:       prRef.Number,
				SuiteID:        p.CheckSuite.ID,
				HeadSha:        p.CheckSuite.HeadSHA,
				AppID:          p.CheckSuite.App.ID,
				Conclusion:     strToText(p.CheckSuite.Conclusion),
				Status:         p.CheckSuite.Status,
				SuiteUpdatedAt: updatedAt,
			}); err != nil {
				slog.Warn("github: stash pending check_suite failed",
					"err", err, "suite_id", p.CheckSuite.ID)
			}
			continue
		}
		if err := h.Queries.UpsertPullRequestCheckSuite(ctx, db.UpsertPullRequestCheckSuiteParams{
			PrID:       pr.ID,
			SuiteID:    p.CheckSuite.ID,
			HeadSha:    p.CheckSuite.HeadSHA,
			AppID:      p.CheckSuite.App.ID,
			Conclusion: strToText(p.CheckSuite.Conclusion),
			Status:     p.CheckSuite.Status,
			UpdatedAt:  updatedAt,
		}); err != nil {
			slog.Warn("github: upsert check_suite failed", "err", err, "suite_id", p.CheckSuite.ID)
			continue
		}
		affectedWorkspaces[uuidToString(pr.WorkspaceID)] = struct{}{}
		issues, err := h.Queries.ListIssueIDsForPullRequest(ctx, pr.ID)
		if err == nil {
			for _, id := range issues {
				affectedIssues[uuidToString(id)] = struct{}{}
			}
		}
	}

	// Broadcast on the existing event so the issue page just re-queries
	// the PR list. We don't pass a single pull_request payload here
	// because a suite can touch several and the listener already
	// invalidates by issue.
	for ws := range affectedWorkspaces {
		linked := make([]string, 0, len(affectedIssues))
		for id := range affectedIssues {
			linked = append(linked, id)
		}
		h.publish(protocol.EventPullRequestUpdated, ws, "system", "", map[string]any{
			"linked_issue_ids": linked,
		})
	}
}

// replayPendingCheckSuitesForPR drains the stash table for one PR (any
// rows left there by a check_suite event that arrived before the PR row
// was mirrored) and re-applies each event through the normal upsert
// path. Safe to call on every PR upsert: the drain is a single
// DELETE … RETURNING, so when there is nothing to replay the helper is
// a no-op round-trip.
func (h *Handler) replayPendingCheckSuitesForPR(ctx context.Context, pr db.GithubPullRequest, workspaceID pgtype.UUID) {
	pending, err := h.Queries.DrainPendingCheckSuitesForPR(ctx, db.DrainPendingCheckSuitesForPRParams{
		WorkspaceID: workspaceID,
		RepoOwner:   pr.RepoOwner,
		RepoName:    pr.RepoName,
		PrNumber:    pr.PrNumber,
	})
	if err != nil {
		slog.Warn("github: drain pending check_suites failed",
			"err", err, "pr_id", uuidToString(pr.ID))
		return
	}
	for _, row := range pending {
		if err := h.Queries.UpsertPullRequestCheckSuite(ctx, db.UpsertPullRequestCheckSuiteParams{
			PrID:       pr.ID,
			SuiteID:    row.SuiteID,
			HeadSha:    row.HeadSha,
			AppID:      row.AppID,
			Conclusion: row.Conclusion,
			Status:     row.Status,
			UpdatedAt:  row.SuiteUpdatedAt,
		}); err != nil {
			slog.Warn("github: replay pending check_suite failed",
				"err", err, "pr_id", uuidToString(pr.ID),
				"suite_id", row.SuiteID)
		}
	}
}

// derivePRMergeableState resolves the upsert behaviour for the PR row's
// mergeable_state column on a `pull_request` webhook. It returns three
// states encoded as (value, clear):
//
//   - clear=true → force the column to NULL. State-changing actions (`opened`,
//     `synchronize`, `reopened`, or a base-branch swap) must blank the value
//     because GitHub re-computes mergeability asynchronously; the payload may
//     still carry the previous head's clean/dirty answer, and trusting it
//     would surface a stale verdict against the new head.
//   - clear=false, value valid → write the value. The event carried a
//     concrete verdict we should persist.
//   - clear=false, value invalid → preserve the existing column. Metadata
//     events (labeled/assigned/edited-without-base-swap) ship pull_request
//     payloads with mergeable_state empty even when the previous verdict is
//     still accurate, and silently overwriting clean/dirty with NULL would
//     drop information GitHub only refreshes lazily.
func derivePRMergeableState(action, payload string, baseRefChanged bool) (pgtype.Text, bool) {
	if action == "opened" || action == "synchronize" || action == "reopened" {
		return pgtype.Text{}, true
	}
	if action == "edited" && baseRefChanged {
		return pgtype.Text{}, true
	}
	if payload == "" {
		return pgtype.Text{}, false
	}
	return pgtype.Text{String: payload, Valid: true}, false
}

// ghPRChanges captures the only field of `pull_request.edited`'s `changes`
// payload we care about: a base-branch swap. Everything else (title, body)
// leaves mergeability intact.
type ghPRChanges struct {
	Base *struct {
		Ref *struct {
			From string `json:"from"`
		} `json:"ref"`
	} `json:"base"`
}

// baseRefChanged returns true when a pull_request.edited event indicates the
// PR's base branch was swapped. Only this kind of edit invalidates the
// existing mergeable_state.
func baseRefChanged(c *ghPRChanges) bool {
	return c != nil && c.Base != nil && c.Base.Ref != nil && c.Base.Ref.From != ""
}

func derivePRState(state string, draft, merged bool) string {
	if merged {
		return "merged"
	}
	if state == "closed" {
		return "closed"
	}
	if draft {
		return "draft"
	}
	return "open"
}

func parseGHTime(s string) pgtype.Timestamptz {
	if s == "" {
		return pgtype.Timestamptz{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func parseGHTimeRequired(s string) pgtype.Timestamptz {
	t := parseGHTime(s)
	if !t.Valid {
		return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	return t
}

func parseStrictUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return u, nil
}

func coalesce(a, fallback string) string {
	if strings.TrimSpace(a) == "" {
		return fallback
	}
	return a
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}
