package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestCommentTombstoneRollsBackWhenOutboxRejectsEvent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := dbfx.Issue(t, "tombstone outbox atomicity")
	root := dbfx.Comment(t, issue, "body must survive failed deletion")
	reply := dbfx.Comment(t, issue, "reply must survive", testutil.Cols{"parent_id": root})
	attachment := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issue, "comment_id": root,
		"uploader_type": "member", "uploader_id": testUserID, "filename": "fixture.txt",
		"url": "https://example.test/fixture.txt", "content_type": "text/plain", "size_bytes": 1,
	})
	comment, err := testHandler.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{ID: parseUUID(root), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil {
		t.Fatal(err)
	}
	next := comment
	next.Revision++
	collision := buildCommentMutationEvent(next, protocol.EventCommentUpdated, "member", testUserID, 0)
	collision.Payload = map[string]any{"fixture": "intentional different event at the same identity"}
	seeded, err := eventoutbox.Enqueue(ctx, testHandler.Queries, collision)
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Cleanup(t, `DELETE FROM domain_event_outbox WHERE idempotency_key=$1`, seeded.IdempotencyKey)
	rec := newCommentEventRecorder()
	rec.delete(t, root).Want(http.StatusInternalServerError)
	if got := loadCommentRowState(t, root); !got.exists || got.deleted || got.content != comment.Content {
		t.Fatalf("failed outbox changed root: %+v", got)
	}
	if !loadCommentRowState(t, reply).exists {
		t.Fatal("failed deletion removed reply")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id=$1`, attachment); n != 1 {
		t.Fatal("failed deletion removed attachment")
	}
	if len(rec.events) != 0 {
		t.Fatal("uncommitted deletion published an event")
	}
	dbfx.Exec(t, `DELETE FROM domain_event_outbox WHERE idempotency_key=$1`, seeded.IdempotencyKey)
	rec.delete(t, root).Want(http.StatusNoContent)
	if got := loadCommentRowState(t, root); !got.deleted {
		t.Fatalf("retry did not tombstone root: %+v", got)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM domain_event_outbox WHERE idempotency_key=$1`, seeded.IdempotencyKey); n != 1 {
		t.Fatalf("successful deletion persisted %d events, want one", n)
	}
	if len(rec.events) != 1 || rec.events[0].Type != protocol.EventCommentUpdated {
		t.Fatalf("committed events: %+v", rec.events)
	}
}
