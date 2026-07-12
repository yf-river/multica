package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type failNamedQueryDB struct {
	db.DBTX
	queryName string
}

func (f failNamedQueryDB) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	if strings.Contains(sql, "-- name: "+f.queryName+" ") {
		return nil, errors.New("injected comment enrichment failure")
	}
	return f.DBTX.Query(ctx, sql, args...)
}

func TestListCommentsPreservesEnrichmentFailures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fixture := newSeededCommentIssue(t, "comment enrichment failure")
	fixture.insertComment(t, nil, 0, "visible comment")

	for _, queryName := range []string{"ListReactionsByCommentIDs", "ListAttachmentsByCommentIDs"} {
		t.Run(queryName, func(t *testing.T) {
			h := *testHandler
			h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: queryName})
			req := newRequest(http.MethodGet, "/api/issues/"+fixture.IssueID+"/comments", nil)
			req = withURLParam(req, "id", fixture.IssueID)
			w := httptest.NewRecorder()

			h.ListComments(w, req)

			if w.Code != http.StatusInternalServerError {
				t.Fatalf("%s failure: expected 500, got %d: %s", queryName, w.Code, w.Body.String())
			}
		})
	}
}
