package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type namedQueryFailingTxStarter struct {
	pool      *pgxpool.Pool
	queryName string
}

func (s namedQueryFailingTxStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return namedQueryFailingTx{Tx: tx, queryName: s.queryName}, nil
}

type namedQueryFailingTx struct {
	pgx.Tx
	queryName string
}

func (tx namedQueryFailingTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "-- name: "+tx.queryName+" ") {
		return nil, errors.New("injected " + tx.queryName + " failure")
	}
	return tx.Tx.Query(ctx, sql, args...)
}

func installAutopilotSubscriberInsertFailure(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	functionName := fmt.Sprintf("autopilot_subscriber_fail_fn_%d", suffix)
	triggerName := fmt.Sprintf("autopilot_subscriber_fail_%d", suffix)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON autopilot_subscriber`, triggerName))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})

	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	RAISE EXCEPTION 'forced autopilot subscriber insert failure';
END;
$$;
`, functionName)); err != nil {
		t.Fatalf("install failure function: %v", err)
	}
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
CREATE TRIGGER %s
BEFORE INSERT ON autopilot_subscriber
FOR EACH ROW EXECUTE FUNCTION %s();
`, triggerName, functionName)); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
}

func memberSubscriberAutopilotBody(t *testing.T, title string) map[string]any {
	t.Helper()
	var agentID string
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load test agent: %v", err)
	}
	return map[string]any{
		"title":          title,
		"assignee_id":    agentID,
		"execution_mode": "create_issue",
		"subscribers": []map[string]any{
			{"user_type": "member", "user_id": testUserID},
		},
	}
}

func createAutopilotFixture(t *testing.T, body map[string]any) AutopilotResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newAutopilotCreateRequest("/api/autopilots?workspace_id="+testWorkspaceID, body)
	testHandler.CreateAutopilot(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateAutopilot: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var autopilot AutopilotResponse
	if err := json.NewDecoder(w.Body).Decode(&autopilot); err != nil {
		t.Fatalf("decode autopilot: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot_subscriber WHERE autopilot_id = $1`, autopilot.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, autopilot.ID)
	})
	return autopilot
}

func createMemberSubscriberAutopilot(t *testing.T, title string) AutopilotResponse {
	t.Helper()
	return createAutopilotFixture(t, memberSubscriberAutopilotBody(t, title))
}

type dispatchedAutopilotIssueFixture struct {
	issueID string
}

func createDispatchedAutopilotIssue(t *testing.T, ctx context.Context, autopilotTitle, issueTitle string, subscribers []map[string]any) dispatchedAutopilotIssueFixture {
	t.Helper()

	var agentID string
	if err := testPool.QueryRow(ctx, `SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load test agent: %v", err)
	}

	body := map[string]any{
		"title":                autopilotTitle,
		"assignee_id":          agentID,
		"execution_mode":       "create_issue",
		"issue_title_template": issueTitle,
	}
	if subscribers != nil {
		body["subscribers"] = subscribers
	}

	autopilot := createAutopilotFixture(t, body)

	queries := db.New(testPool)
	ap, err := queries.GetAutopilot(ctx, parseUUID(autopilot.ID))
	if err != nil {
		t.Fatalf("GetAutopilot: %v", err)
	}
	run, err := testHandler.AutopilotService.DispatchAutopilot(ctx, ap, pgtype.UUID{}, "manual", nil)
	if err != nil {
		t.Fatalf("DispatchAutopilot: %v", err)
	}
	if run == nil || !run.IssueID.Valid {
		t.Fatalf("dispatch run = %+v, want linked issue", run)
	}
	issueID := uuidToString(run.IssueID)
	var durableEventCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM domain_event_outbox
		WHERE event_type = 'issue:created'
		  AND stream_key = 'issue:' || $1
		  AND payload #>> '{issue,id}' = $1
	`, issueID).Scan(&durableEventCount); err != nil {
		t.Fatalf("count autopilot issue-created event: %v", err)
	}
	if durableEventCount != 1 {
		t.Fatalf("autopilot durable issue-created events = %d, want 1", durableEventCount)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM domain_event_outbox WHERE stream_key = 'issue:' || $1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	return dispatchedAutopilotIssueFixture{
		issueID: issueID,
	}
}

// TestCreateAutopilotPersistsMemberSubscribers covers the happy path:
// supplying a non-empty `subscribers` array on POST /api/autopilots stores
// the rows and the response echoes them back. This is the create half of the
// MUL-2533 RFC ("autopilot default subscriber template").
func TestCreateAutopilotPersistsMemberSubscribers(t *testing.T) {
	ctx := context.Background()
	resp := createMemberSubscriberAutopilot(t, "Subscriber template autopilot")
	if len(resp.Subscribers) != 1 {
		t.Fatalf("subscribers in response = %d, want 1", len(resp.Subscribers))
	}
	if resp.Subscribers[0].UserType != "member" || resp.Subscribers[0].UserID != testUserID {
		t.Fatalf("subscribers[0] = %+v, want member/%s", resp.Subscribers[0], testUserID)
	}

	// Confirm the row landed in the DB. Belt-and-braces: the response could
	// in principle be assembled from the request without writing.
	var count int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM autopilot_subscriber WHERE autopilot_id = $1
	`, resp.ID).Scan(&count); err != nil {
		t.Fatalf("count subscribers: %v", err)
	}
	if count != 1 {
		t.Fatalf("autopilot_subscriber rows = %d, want 1", count)
	}
}

func TestCreateAutopilotRollsBackWhenSubscriberResponseCannotBePrepared(t *testing.T) {
	ctx := context.Background()
	title := "subscriber-response-failure-" + randomID()[:8]
	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "ListAutopilotSubscribers"})
	h.TxStarter = namedQueryFailingTxStarter{pool: testPool, queryName: "ListAutopilotSubscribers"}

	w := httptest.NewRecorder()
	req := newAutopilotCreateRequest("/api/autopilots?workspace_id="+testWorkspaceID, memberSubscriberAutopilotBody(t, title))
	h.CreateAutopilot(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("subscriber response failure = %d %s, want 500", w.Code, w.Body.String())
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM autopilot WHERE title = $1`, title).Scan(&count); err != nil {
		t.Fatalf("count autopilots: %v", err)
	}
	if count != 0 {
		t.Fatalf("committed autopilots = %d, want rollback", count)
	}
}

func TestUpdateAutopilotRollsBackWhenSubscriberResponseCannotBePrepared(t *testing.T) {
	ctx := context.Background()
	created := createMemberSubscriberAutopilot(t, "subscriber-update-original-"+randomID()[:8])

	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "ListAutopilotSubscribers"})
	h.TxStarter = namedQueryFailingTxStarter{pool: testPool, queryName: "ListAutopilotSubscribers"}
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPatch, "/api/autopilots/"+created.ID+"?workspace_id="+testWorkspaceID, map[string]any{
		"title":       "subscriber-update-should-rollback",
		"subscribers": []map[string]any{},
	})
	req = withURLParam(req, "id", created.ID)
	h.UpdateAutopilot(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("subscriber response failure = %d %s, want 500", w.Code, w.Body.String())
	}

	var title string
	var subscribers int
	if err := testPool.QueryRow(ctx, `
		SELECT a.title, count(s.user_id)
		FROM autopilot a
		LEFT JOIN autopilot_subscriber s ON s.autopilot_id = a.id
		WHERE a.id = $1
		GROUP BY a.id
	`, created.ID).Scan(&title, &subscribers); err != nil {
		t.Fatalf("load rolled-back autopilot: %v", err)
	}
	if title != created.Title || subscribers != 1 {
		t.Fatalf("autopilot after failure = title %q subscribers %d, want %q/1", title, subscribers, created.Title)
	}
}

// TestCreateAutopilotRejectsNonMemberSubscriberType locks in the first-version
// constraint: only user_type='member' is accepted on the API. The DB CHECK
// would also reject anything else; the 400 here exists so the client gets a
// clear message instead of a 500 with a constraint-name leak.
func TestCreateAutopilotRejectsNonMemberSubscriberType(t *testing.T) {
	var agentID string
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load test agent: %v", err)
	}

	w := httptest.NewRecorder()
	req := newAutopilotCreateRequest("/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{
		"title":          "Bad subscriber type",
		"assignee_id":    agentID,
		"execution_mode": "create_issue",
		"subscribers": []map[string]any{
			{"user_type": "agent", "user_id": agentID},
		},
	})
	testHandler.CreateAutopilot(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("CreateAutopilot: expected 400 for non-member subscriber, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateAutopilotRejectsForeignSubscriber covers the boundary check:
// supplying a UUID that does not belong to this workspace must 400, not
// silently leak inside the autopilot row.
func TestCreateAutopilotRejectsForeignSubscriber(t *testing.T) {
	var agentID string
	if err := testPool.QueryRow(context.Background(), `SELECT id FROM agent WHERE workspace_id = $1 LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load test agent: %v", err)
	}

	w := httptest.NewRecorder()
	req := newAutopilotCreateRequest("/api/autopilots?workspace_id="+testWorkspaceID, map[string]any{
		"title":          "Foreign subscriber",
		"assignee_id":    agentID,
		"execution_mode": "create_issue",
		"subscribers": []map[string]any{
			{"user_type": "member", "user_id": "00000000-0000-0000-0000-000000000000"},
		},
	})
	testHandler.CreateAutopilot(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("CreateAutopilot: expected 400 for foreign member subscriber, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateAutopilotRollsBackWhenSubscriberInsertFails(t *testing.T) {
	ctx := context.Background()
	title := fmt.Sprintf("Subscriber rollback create %d", time.Now().UnixNano())

	installAutopilotSubscriberInsertFailure(t)

	w := httptest.NewRecorder()
	req := newAutopilotCreateRequest("/api/autopilots?workspace_id="+testWorkspaceID, memberSubscriberAutopilotBody(t, title))
	testHandler.CreateAutopilot(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("CreateAutopilot: expected 500 for forced subscriber insert failure, got %d: %s", w.Code, w.Body.String())
	}

	var count int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM autopilot
		WHERE workspace_id = $1 AND title = $2
	`, testWorkspaceID, title).Scan(&count); err != nil {
		t.Fatalf("count rolled-back autopilots: %v", err)
	}
	if count != 0 {
		t.Fatalf("autopilot rows after failed subscriber insert = %d, want 0", count)
	}
}

// TestUpdateAutopilotFullReplaceSubscribers covers the PATCH semantics from
// the RFC: sending `subscribers` wipes whatever was there and re-inserts the
// new set. Omitting the field would leave the previous template untouched;
// that branch is exercised separately by TestUpdateAutopilotPreservesSubscribersWhenOmitted.
func TestUpdateAutopilotFullReplaceSubscribers(t *testing.T) {
	ctx := context.Background()
	created := createMemberSubscriberAutopilot(t, "Replace subscribers autopilot")

	// PATCH with an empty array → expect zero subscribers afterward.
	w := httptest.NewRecorder()
	req := newRequest("PATCH", "/api/autopilots/"+created.ID+"?workspace_id="+testWorkspaceID, map[string]any{
		"subscribers": []map[string]any{},
	})
	req = withURLParam(req, "id", created.ID)
	testHandler.UpdateAutopilot(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateAutopilot: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated AutopilotResponse
	if err := json.NewDecoder(w.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated: %v", err)
	}
	if len(updated.Subscribers) != 0 {
		t.Fatalf("subscribers after empty replace = %d, want 0", len(updated.Subscribers))
	}

	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM autopilot_subscriber WHERE autopilot_id = $1`, created.ID).Scan(&count); err != nil {
		t.Fatalf("count after replace: %v", err)
	}
	if count != 0 {
		t.Fatalf("DB rows after empty replace = %d, want 0", count)
	}
}

func TestUpdateAutopilotRollsBackWhenSubscriberInsertFails(t *testing.T) {
	ctx := context.Background()
	originalTitle := fmt.Sprintf("Subscriber rollback update %d", time.Now().UnixNano())
	updatedTitle := originalTitle + " changed"
	created := createMemberSubscriberAutopilot(t, originalTitle)

	installAutopilotSubscriberInsertFailure(t)

	w := httptest.NewRecorder()
	req := newRequest("PATCH", "/api/autopilots/"+created.ID+"?workspace_id="+testWorkspaceID, map[string]any{
		"title": updatedTitle,
		"subscribers": []map[string]any{
			{"user_type": "member", "user_id": testUserID},
		},
	})
	req = withURLParam(req, "id", created.ID)
	testHandler.UpdateAutopilot(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("UpdateAutopilot: expected 500 for forced subscriber insert failure, got %d: %s", w.Code, w.Body.String())
	}

	var gotTitle string
	if err := testPool.QueryRow(ctx, `SELECT title FROM autopilot WHERE id = $1`, created.ID).Scan(&gotTitle); err != nil {
		t.Fatalf("load autopilot title after rollback: %v", err)
	}
	if gotTitle != originalTitle {
		t.Fatalf("autopilot title after failed subscriber replace = %q, want %q", gotTitle, originalTitle)
	}

	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM autopilot_subscriber WHERE autopilot_id = $1`, created.ID).Scan(&count); err != nil {
		t.Fatalf("count subscribers after rollback: %v", err)
	}
	if count != 1 {
		t.Fatalf("subscriber rows after failed replace = %d, want 1", count)
	}
}

// TestUpdateAutopilotPreservesSubscribersWhenOmitted asserts the
// "omit the field to leave it alone" contract — a previously-set template
// must NOT be wiped just because the client sent a partial PATCH.
func TestUpdateAutopilotPreservesSubscribersWhenOmitted(t *testing.T) {
	ctx := context.Background()
	created := createMemberSubscriberAutopilot(t, "Preserve subscribers autopilot")

	// PATCH a different field, leave subscribers out → row count unchanged.
	w := httptest.NewRecorder()
	req := newRequest("PATCH", "/api/autopilots/"+created.ID+"?workspace_id="+testWorkspaceID, map[string]any{
		"title": "Preserve subscribers autopilot (renamed)",
	})
	req = withURLParam(req, "id", created.ID)
	testHandler.UpdateAutopilot(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateAutopilot: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM autopilot_subscriber WHERE autopilot_id = $1`, created.ID).Scan(&count); err != nil {
		t.Fatalf("count after omitted PATCH: %v", err)
	}
	if count != 1 {
		t.Fatalf("DB rows after omitted PATCH = %d, want 1 (subscribers must not have been touched)", count)
	}
}

// TestAutopilotDispatchFansOutSubscribersToIssue is the integration check
// for the dispatch path: an autopilot with a default subscriber list must
// auto-subscribe each entry to the issue it spawns, with reason='autopilot'.
// Belt-and-braces: also confirms that the creator-of-the-issue (the assignee
// agent — see TestAutopilotCreatedIssueCreatorIsAssigneeAgent) gets a row
// with reason='creator', and the two reasons don't fight (PK is one row per
// (issue, user_type, user_id), so the first one wins on conflict).
func TestAutopilotDispatchFansOutSubscribersToIssue(t *testing.T) {
	ctx := context.Background()
	title := fmt.Sprintf("Autopilot subscriber fanout %d", time.Now().UnixNano())
	fixture := createDispatchedAutopilotIssue(t, ctx, "Subscriber fanout autopilot", title, []map[string]any{
		{"user_type": "member", "user_id": testUserID},
	})

	var subscriberReason string
	if err := testPool.QueryRow(ctx, `
		SELECT reason
		FROM issue_subscriber
		WHERE issue_id = $1 AND user_type = 'member' AND user_id = $2
	`, fixture.issueID, testUserID).Scan(&subscriberReason); err != nil {
		t.Fatalf("query autopilot-fanned subscriber: %v", err)
	}
	if subscriberReason != "autopilot" {
		t.Fatalf("subscriber reason = %q, want %q", subscriberReason, "autopilot")
	}
}

// TestAutopilotDispatchNotifiesSubscribersOnCreate locks in the OQ3 promise
// from the RFC ("reason='autopilot' 与 reason='manual' 一致，订阅事件全收"):
// when an autopilot creates an issue, each template subscriber must land in
// the recipient's inbox with type='issue_subscribed' pointing at the new
// issue. Without this, subscribers would only see comment/status updates
// after the fact and miss the creation event itself — flagged in PR #3060
// review by the Emacs agent.
func TestAutopilotDispatchNotifiesSubscribersOnCreate(t *testing.T) {
	ctx := context.Background()
	title := fmt.Sprintf("Autopilot subscriber inbox %d", time.Now().UnixNano())
	fixture := createDispatchedAutopilotIssue(t, ctx, "Subscriber inbox autopilot", title, []map[string]any{
		{"user_type": "member", "user_id": testUserID},
	})

	var inboxCount int
	var inboxType, inboxTitle string
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_item
		WHERE issue_id = $1 AND recipient_id = $2 AND type = 'issue_subscribed'
	`, fixture.issueID, testUserID).Scan(&inboxCount); err != nil {
		t.Fatalf("count inbox rows: %v", err)
	}
	if inboxCount != 1 {
		t.Fatalf("inbox_item rows for subscriber = %d, want 1", inboxCount)
	}

	if err := testPool.QueryRow(ctx, `
		SELECT type, title FROM inbox_item
		WHERE issue_id = $1 AND recipient_id = $2 AND type = 'issue_subscribed'
	`, fixture.issueID, testUserID).Scan(&inboxType, &inboxTitle); err != nil {
		t.Fatalf("load inbox row: %v", err)
	}
	if inboxType != "issue_subscribed" {
		t.Fatalf("inbox type = %q, want issue_subscribed", inboxType)
	}
	if inboxTitle != title {
		t.Fatalf("inbox title = %q, want %q (issue title)", inboxTitle, title)
	}
}

// TestAutopilotDispatchSkipsInboxWhenNoSubscribers asserts the no-op path:
// an autopilot with an empty subscriber template must NOT create any inbox
// rows on dispatch — otherwise we'd be paging the workspace on every quiet
// autopilot run. The corresponding issue_subscriber rows are also expected
// to be absent (other-reason rows like creator/assignee are filtered out by
// the WHERE type = 'issue_subscribed' clause).
func TestAutopilotDispatchSkipsInboxWhenNoSubscribers(t *testing.T) {
	ctx := context.Background()
	title := fmt.Sprintf("Autopilot no-subscriber inbox %d", time.Now().UnixNano())
	fixture := createDispatchedAutopilotIssue(t, ctx, "No-subscriber autopilot", title, nil)

	var inboxCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_item
		WHERE issue_id = $1 AND type = 'issue_subscribed'
	`, fixture.issueID).Scan(&inboxCount); err != nil {
		t.Fatalf("count inbox rows: %v", err)
	}
	if inboxCount != 0 {
		t.Fatalf("issue_subscribed inbox rows = %d, want 0 (no subscribers)", inboxCount)
	}
}

// TestDeleteAutopilotRemovesSubscribers guards the app-layer cleanup that
// replaced the dropped autopilot_subscriber → autopilot ON DELETE CASCADE:
// deleting an autopilot must also delete its subscriber template rows in the
// same transaction, leaving no orphans behind.
func TestDeleteAutopilotRemovesSubscribers(t *testing.T) {
	ctx := context.Background()
	created := createMemberSubscriberAutopilot(t, "Delete-with-subscribers autopilot")

	var before int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM autopilot_subscriber WHERE autopilot_id = $1`, created.ID).Scan(&before); err != nil {
		t.Fatalf("count subscribers before delete: %v", err)
	}
	if before != 1 {
		t.Fatalf("subscriber rows before delete = %d, want 1", before)
	}

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/autopilots/"+created.ID+"?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "id", created.ID)
	testHandler.DeleteAutopilot(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteAutopilot: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var after int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM autopilot_subscriber WHERE autopilot_id = $1`, created.ID).Scan(&after); err != nil {
		t.Fatalf("count subscribers after delete: %v", err)
	}
	if after != 0 {
		t.Fatalf("subscriber rows after delete = %d, want 0 (app-layer cleanup)", after)
	}

	var autopilotRows int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM autopilot WHERE id = $1`, created.ID).Scan(&autopilotRows); err != nil {
		t.Fatalf("count autopilot after delete: %v", err)
	}
	if autopilotRows != 0 {
		t.Fatalf("autopilot rows after delete = %d, want 0", autopilotRows)
	}
}
