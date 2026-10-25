package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Use the real removal handlers, including their transactional subscription
// cleanup, rather than deleting only the member row in the regression fixture.
func removeNotificationMember(t *testing.T, userID, memberID, method string) {
	t.Helper()
	h := &handler.Handler{Queries: db.New(testPool), TxStarter: testPool, Bus: events.New()}
	r := httptest.NewRequest(http.MethodDelete, "/", nil)
	actor := testUserID
	if method == "leave" {
		actor = userID
	}
	r.Header.Set("X-User-ID", actor)
	ctx := r.Context()
	route := chi.NewRouteContext()
	route.URLParams.Add("id", testWorkspaceID)
	route.URLParams.Add("memberId", memberID)
	r = r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
	if method == "leave" {
		testutil.Call(t, h.LeaveWorkspace, r).Want(http.StatusNoContent)
	} else {
		testutil.Call(t, h.DeleteMember, r).Want(http.StatusNoContent)
	}
	fx := workspaceFixture(t)
	if n := fx.Count(t, `SELECT count(*) FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, userID); n != 0 {
		t.Fatalf("removal left %d member rows", n)
	}
}

func TestNotification_CurrentMembership(t *testing.T) {
	for _, source := range []string{
		"issue reaction", "comment reaction", "created assignee", "updated assignee", "previous assignee",
		"description mention created", "description mention updated", "comment mention",
		"description squad created", "description squad updated", "comment squad",
		"all", "subscriber comment", "subscriber status", "subscriber task failure", "parent subscriber",
	} {
		for _, state := range []string{"current", "delete", "leave"} {
			removed := state != "current"
			t.Run(source+"/"+state, func(t *testing.T) {
				fx := workspaceFixture(t)
				recipient := fx.User(t, "Recipient", "membership-recipient@example.test")
				memberID := fx.Member(t, testWorkspaceID, recipient, "member")
				issueID := fx.Issue(t, "Membership regression", testutil.Cols{"creator_id": recipient})
				commentID := fx.Comment(t, issueID, "Original comment", testutil.Cols{"author_id": recipient})
				fx.Cleanup(t, `DELETE FROM inbox_item WHERE issue_id = $1`, issueID)
				fx.Cleanup(t, `DELETE FROM issue_subscriber WHERE issue_id = $1`, issueID)
				memberType := "member"
				issue := handler.IssueResponse{ID: issueID, WorkspaceID: testWorkspaceID, Title: "Membership regression", Status: "todo", CreatorType: "member", CreatorID: recipient}
				e := events.Event{WorkspaceID: testWorkspaceID, ActorType: "member", ActorID: testUserID}
				payload := map[string]any{"issue_id": issueID, "issue_title": issue.Title, "issue_status": issue.Status}
				expectedType := "mentioned"
				content := "[@Recipient](mention://member/" + recipient + ")"
				if source == "description squad created" || source == "description squad updated" || source == "comment squad" {
					agent := fx.Agent(t, "Leader", "")
					squad := fx.Squad(t, "Squad", agent)
					fx.SquadMember(t, squad, "member", recipient)
					content = "[@Squad](mention://squad/" + squad + ")"
				}
				switch source {
				case "issue reaction":
					e.Type = protocol.EventIssueReactionAdded
					payload["reaction"] = handler.IssueReactionResponse{ID: commentID, IssueID: issueID, ActorType: "member", ActorID: testUserID, Emoji: "👍"}
					payload["creator_type"], payload["creator_id"] = "member", recipient
					expectedType = "reaction_added"
				case "comment reaction":
					e.Type = protocol.EventReactionAdded
					payload["reaction"] = handler.ReactionResponse{ID: commentID, CommentID: commentID, ActorType: "member", ActorID: testUserID, Emoji: "👍"}
					payload["comment_author_type"], payload["comment_author_id"], payload["comment_id"] = "member", recipient, commentID
					expectedType = "reaction_added"
				case "created assignee", "updated assignee", "previous assignee":
					e.Type = protocol.EventIssueCreated
					issue.AssigneeType, issue.AssigneeID = &memberType, &recipient
					expectedType = "issue_assigned"
					if source != "created assignee" {
						e.Type = protocol.EventIssueUpdated
						payload["assignee_changed"] = true
					}
					if source == "previous assignee" {
						issue.AssigneeType, issue.AssigneeID = nil, nil
						payload["prev_assignee_type"], payload["prev_assignee_id"] = &memberType, &recipient
						expectedType = "unassigned"
					}
					payload["issue"] = issue
				case "description mention created", "description squad created", "description mention updated", "description squad updated":
					e.Type = protocol.EventIssueCreated
					if source == "description mention updated" || source == "description squad updated" {
						e.Type = protocol.EventIssueUpdated
						payload["description_changed"] = true
					}
					issue.Description = &content
					payload["issue"] = issue
				case "comment mention", "comment squad", "all", "subscriber comment":
					e.Type = protocol.EventCommentCreated
					if source == "all" {
						content = "[@All](mention://all/all)"
					}
					if source == "subscriber comment" {
						content = "No mentions"
						addTestSubscriber(t, issueID, "member", recipient, "manual")
						expectedType = "new_comment"
					}
					payload["comment"] = handler.CommentResponse{ID: commentID, IssueID: issueID, AuthorType: "member", AuthorID: testUserID, Content: content}
				case "subscriber status", "parent subscriber":
					subscriberIssue := issueID
					if source == "parent subscriber" {
						subscriberIssue = fx.Issue(t, "Parent")
						fx.Exec(t, `UPDATE issue SET parent_issue_id = $1 WHERE id = $2`, subscriberIssue, issueID)
						fx.Cleanup(t, `DELETE FROM issue_subscriber WHERE issue_id = $1`, subscriberIssue)
					}
					addTestSubscriber(t, subscriberIssue, "member", recipient, "manual")
					e.Type = protocol.EventIssueUpdated
					issue.Status = "done"
					payload["issue"], payload["status_changed"], payload["prev_status"] = issue, true, "todo"
					expectedType = "status_changed"
				case "subscriber task failure":
					addTestSubscriber(t, issueID, "member", recipient, "manual")
					e.Type = protocol.EventTaskFailed
					expectedType = "task_failed"
				}
				if removed {
					removeNotificationMember(t, recipient, memberID, state)
					if n := fx.Count(t, `SELECT count(*) FROM issue_subscriber WHERE user_id = $1`, recipient); n != 0 {
						t.Fatalf("removal left %d subscriptions", n)
					}
				}
				// Register only notification listeners: source events are delivered
				// synchronously, with no sleeps or asynchronous observation window.
				bus := events.New()
				registerNotificationListeners(bus, db.New(testPool))
				eventCount := 0
				bus.Subscribe(protocol.EventInboxNew, func(e events.Event) {
					item := e.Payload.(map[string]any)["item"].(map[string]any)
					if item["recipient_id"] == recipient {
						eventCount++
						if item["type"] != expectedType {
							t.Errorf("event type = %v, want %s", item["type"], expectedType)
						}
					}
				})
				e.Payload = payload
				if source == "issue reaction" || source == "comment reaction" {
					consume := consumeIssueReactionNotification
					if source == "comment reaction" {
						consume = consumeCommentReactionNotification
					}
					derived, err := consume(context.Background(), db.New(testPool), e)
					if err != nil {
						t.Fatal(err)
					}
					for _, event := range derived {
						bus.Publish(event)
					}
				} else {
					bus.Publish(e)
				}
				want := 1
				if removed {
					want = 0
				}
				if n := fx.Count(t, `SELECT count(*) FROM inbox_item WHERE workspace_id = $1 AND recipient_id = $2`, testWorkspaceID, recipient); n != want {
					t.Errorf("inbox rows = %d, want %d", n, want)
				}
				if eventCount != want {
					t.Errorf("inbox:new events = %d, want %d", eventCount, want)
				}
			})
		}
	}
}

// Only the origin lookup is fault-injected to select the unconfirmed branch;
// task transitions and inbox writes still run against real PostgreSQL.
type originLookupFailure struct{ *pgxpool.Pool }
type failedOriginRow struct{}

func (failedOriginRow) Scan(...any) error { return errors.New("injected origin lookup failure") }
func (q originLookupFailure) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.HasPrefix(sql, "-- name: GetIssueByOrigin ") {
		return failedOriginRow{}
	}
	return q.Pool.QueryRow(ctx, sql, args...)
}

func TestNotification_ServiceMembership(t *testing.T) {
	for _, source := range []string{"quick_create_done", "quick_create_failed", "quick_create_unconfirmed"} {
		for _, state := range []string{"current", "delete", "leave"} {
			t.Run(source+"/"+state, func(t *testing.T) {
				ctx := context.Background()
				fx := workspaceFixture(t)
				recipient := fx.User(t, "Requester", "membership-service@example.test")
				member := fx.Member(t, testWorkspaceID, recipient, "member")
				bus := events.New()
				queries := db.New(testPool)
				if source == "quick_create_unconfirmed" {
					queries = db.New(originLookupFailure{testPool})
				}
				tasks := service.NewTaskService(queries, testPool, nil, bus)
				eventCount := 0
				bus.Subscribe(protocol.EventInboxNew, func(e events.Event) {
					item := e.Payload.(map[string]any)["item"].(map[string]any)
					if item["recipient_id"] == recipient {
						eventCount++
						if item["type"] != source {
							t.Errorf("event type = %v, want %s", item["type"], source)
						}
					}
				})
				fx.Cleanup(t, `DELETE FROM inbox_item WHERE recipient_id = $1`, recipient)
				var trigger func() error
				{
					runtime := fx.Runtime(t, "Shared runtime")
					agent := fx.Agent(t, "Shared agent", runtime)
					qc, err := json.Marshal(service.QuickCreateContext{Type: service.QuickCreateContextType, RequesterID: recipient, WorkspaceID: testWorkspaceID, Prompt: "Create issue"})
					if err != nil {
						t.Fatal(err)
					}
					taskID := fx.Task(t, agent, testutil.Cols{"runtime_id": runtime, "status": "running", "context": qc})
					if source == "quick_create_done" {
						fx.Issue(t, "Created issue", testutil.Cols{"creator_type": "agent", "creator_id": agent, "origin_type": "quick_create", "origin_id": taskID})
					}
					trigger = func() error {
						_, err := tasks.CompleteTask(ctx, util.MustParseUUID(taskID), []byte(`{"output":"Finished"}`), "", "", "", false, "", "")
						return err
					}
				}
				if state != "current" {
					removeNotificationMember(t, recipient, member, state)
				}
				if err := trigger(); err != nil {
					t.Fatalf("trigger: %v", err)
				}
				want := 1
				if state != "current" {
					want = 0
				}
				if n := fx.Count(t, `SELECT count(*) FROM inbox_item WHERE recipient_id = $1`, recipient); n != want {
					t.Errorf("inbox rows = %d, want %d", n, want)
				}
				if eventCount != want {
					t.Errorf("inbox:new events = %d, want %d", eventCount, want)
				}
			})
		}
	}
}
