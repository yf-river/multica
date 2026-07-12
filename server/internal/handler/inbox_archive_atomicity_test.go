package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestArchiveInboxItemRollsBackTargetWhenSiblingArchiveFails(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	var issueID string
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id,number) VALUES ($1,$2,'member',$3,$4) RETURNING id`, testWorkspaceID, fmt.Sprintf("inbox archive atomic %d", suffix), testUserID, nextHandlerTestIssueNumber(t)).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	var targetID, siblingID string
	if err := testPool.QueryRow(ctx, `INSERT INTO inbox_item (workspace_id,recipient_type,recipient_id,type,issue_id,title) VALUES ($1,'member',$2,'mention',$3,$4) RETURNING id`, testWorkspaceID, testUserID, issueID, fmt.Sprintf("target %d", suffix)).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	siblingTitle := fmt.Sprintf("sibling %d", suffix)
	if err := testPool.QueryRow(ctx, `INSERT INTO inbox_item (workspace_id,recipient_type,recipient_id,type,issue_id,title) VALUES ($1,'member',$2,'mention',$3,$4) RETURNING id`, testWorkspaceID, testUserID, issueID, siblingTitle).Scan(&siblingID); err != nil {
		t.Fatal(err)
	}
	const functionName = "test_fail_atomic_inbox_sibling_archive"
	const triggerName = "test_fail_atomic_inbox_sibling_archive_trigger"
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			IF OLD.title=%s AND NEW.archived THEN RAISE EXCEPTION 'injected sibling archive failure'; END IF;
			RETURN NEW;
		END $$;
		DROP TRIGGER IF EXISTS %s ON inbox_item;
		CREATE TRIGGER %s BEFORE UPDATE ON inbox_item FOR EACH ROW EXECUTE FUNCTION %s()
	`, functionName, quoteSQLLiteral(siblingTitle), triggerName, triggerName, functionName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON inbox_item; DROP FUNCTION IF EXISTS %s()`, triggerName, functionName))
		_, _ = testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE id IN ($1,$2)`, targetID, siblingID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID)
	})
	w := httptest.NewRecorder()
	testHandler.ArchiveInboxItem(w, withURLParam(newRequest(http.MethodPost, "/api/inbox/"+targetID+"/archive", nil), "id", targetID))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("archive: expected 500, got %d: %s", w.Code, w.Body.String())
	}
	var archivedCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*)::int FROM inbox_item WHERE id IN ($1,$2) AND archived`, targetID, siblingID).Scan(&archivedCount); err != nil {
		t.Fatal(err)
	}
	if archivedCount != 0 {
		t.Fatalf("failed sibling archive left %d archived rows", archivedCount)
	}
}
