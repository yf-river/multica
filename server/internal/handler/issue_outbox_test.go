package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestIssueCreateAndUpdateCommitDurableEvents(t *testing.T) {
	ctx := context.Background()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "Durable issue event",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode created issue: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM domain_event_outbox WHERE payload #>> '{issue,id}' = $1`, created.ID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, created.ID)
	})

	assertDurableIssueEvent(t, ctx, protocol.EventIssueCreated, created.ID, "Durable issue event")

	w = httptest.NewRecorder()
	req = newRequest("PUT", "/api/issues/"+created.ID, map[string]any{
		"title": "Durable issue event updated",
	})
	req = withURLParam(req, "id", created.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	assertDurableIssueEvent(t, ctx, protocol.EventIssueUpdated, created.ID, "Durable issue event updated")
}

func TestIssueUpdateEventMarksExplicitUnassignAsChange(t *testing.T) {
	created := createIssueForUpdateEvent(t, map[string]any{
		"title":         "Durable explicit unassign event",
		"assignee_type": "member",
		"assignee_id":   testUserID,
	})

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+created.ID, map[string]any{
		"assignee_type": nil,
		"assignee_id":   nil,
	})
	req = withURLParam(req, "id", created.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	flags := latestIssueUpdatedFlags(t, created.ID)
	if !flags.AssigneeChanged {
		t.Fatal("explicit unassign committed without assignee_changed=true")
	}
}

func TestIssueUpdateEventDoesNotMarkUnchangedDescription(t *testing.T) {
	created := createIssueForUpdateEvent(t, map[string]any{
		"title":       "Durable unchanged description event",
		"description": "same description",
	})

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+created.ID, map[string]any{
		"description": "same description",
	})
	req = withURLParam(req, "id", created.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	flags := latestIssueUpdatedFlags(t, created.ID)
	if flags.DescriptionChanged {
		t.Fatal("unchanged description committed with description_changed=true")
	}
}

func TestIssueUpdateRollsBackWhenDurableEventCannotBeInserted(t *testing.T) {
	created := createIssueForUpdateEvent(t, map[string]any{
		"title": "Outbox insertion rollback",
	})
	installOutboxStreamFailure(t, "issue:"+created.ID)

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+created.ID, map[string]any{
		"title": "must not persist",
	})
	req = withURLParam(req, "id", created.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("UpdateIssue: expected 500, got %d: %s", w.Code, w.Body.String())
	}

	var title string
	if err := testPool.QueryRow(context.Background(), `SELECT title FROM issue WHERE id = $1`, created.ID).Scan(&title); err != nil {
		t.Fatalf("load issue after outbox failure: %v", err)
	}
	if title != "Outbox insertion rollback" {
		t.Fatalf("issue title persisted without durable event: %q", title)
	}
	var eventCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = $1 AND payload #>> '{issue,id}' = $2
	`, protocol.EventIssueUpdated, created.ID).Scan(&eventCount); err != nil {
		t.Fatalf("count issue-updated events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("failed update left %d durable events", eventCount)
	}
}

func installOutboxStreamFailure(t *testing.T, streamKey string) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	functionName := "outbox_fail_fn_" + suffix
	triggerName := "outbox_fail_" + suffix
	ctx := context.Background()
	t.Cleanup(func() {
		mustExec(t, ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON domain_event_outbox`, triggerName))
		mustExec(t, ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'forced outbox insertion failure';
		END;
		$$;
		CREATE TRIGGER %s
		BEFORE INSERT ON domain_event_outbox
		FOR EACH ROW WHEN (NEW.stream_key = '%s')
		EXECUTE FUNCTION %s();
	`, functionName, triggerName, streamKey, functionName)); err != nil {
		t.Fatalf("install outbox failure trigger: %v", err)
	}
}

func createIssueForUpdateEvent(t *testing.T, body map[string]any) IssueResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, body)
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode created issue: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM domain_event_outbox WHERE payload #>> '{issue,id}' = $1`, created.ID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, created.ID)
	})
	return created
}

type issueUpdatedFlags struct {
	AssigneeChanged    bool `json:"assignee_changed"`
	DescriptionChanged bool `json:"description_changed"`
}

func latestIssueUpdatedFlags(t *testing.T, issueID string) issueUpdatedFlags {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `
		SELECT payload
		FROM domain_event_outbox
		WHERE event_type = $1
		  AND payload #>> '{issue,id}' = $2
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, protocol.EventIssueUpdated, issueID).Scan(&raw); err != nil {
		t.Fatalf("load latest issue-updated event: %v", err)
	}
	var flags issueUpdatedFlags
	if err := json.Unmarshal(raw, &flags); err != nil {
		t.Fatalf("decode issue-updated event: %v", err)
	}
	return flags
}

func assertDurableIssueEvent(t *testing.T, ctx context.Context, eventType, issueID, title string) {
	t.Helper()
	var count int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM domain_event_outbox
		WHERE event_type = $1
		  AND payload #>> '{issue,id}' = $2
		  AND payload #>> '{issue,title}' = $3
		  AND stream_key = 'issue:' || $2
		  AND processed_at IS NULL
	`, eventType, issueID, title).Scan(&count); err != nil {
		t.Fatalf("count durable %s event: %v", eventType, err)
	}
	if count != 1 {
		t.Fatalf("durable %s events = %d, want 1", eventType, count)
	}
}
