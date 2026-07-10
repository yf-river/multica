package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func loadIssueProjection(
	ctx context.Context,
	queries *db.Queries,
	event events.Event,
	label string,
) (issueEventPayload, bool, error) {
	payload, ok := decodeIssueEvent(event)
	if !ok {
		return issueEventPayload{}, false, fmt.Errorf("decode %s projection payload", label)
	}
	if err := validateIssueProjectionScope(event, payload.Issue); err != nil {
		return issueEventPayload{}, false, err
	}
	exists, err := issueExistsForProjection(ctx, queries, payload.Issue)
	return payload, exists, err
}

func validateIssueProjectionScope(event events.Event, issue eventIssue) error {
	if event.WorkspaceID != "" && event.WorkspaceID != issue.WorkspaceID {
		return fmt.Errorf("event workspace %s does not match issue workspace %s", event.WorkspaceID, issue.WorkspaceID)
	}
	return nil
}

func issueExistsForProjection(ctx context.Context, queries *db.Queries, issue eventIssue) (bool, error) {
	issueID, err := util.ParseUUID(issue.ID)
	if err != nil {
		return false, fmt.Errorf("projection event has invalid issue ID: %w", err)
	}
	workspaceID, err := util.ParseUUID(issue.WorkspaceID)
	if err != nil {
		return false, fmt.Errorf("projection event has invalid workspace ID: %w", err)
	}
	if _, err := queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          issueID,
		WorkspaceID: workspaceID,
	}); errors.Is(err, pgx.ErrNoRows) {
		// The issue was deleted after the primary transaction committed. There
		// is no visible projection left to build, so completing the consumer is
		// correct; retrying an unavoidable foreign-key failure would poison the
		// stream forever.
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("load issue before projection: %w", err)
	}
	return true, nil
}
