package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func createCommentWithKey(t *testing.T, issueID, key string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", body)
	req.Header.Set("Idempotency-Key", key)
	req = withURLParam(req, "id", issueID)
	testHandler.CreateComment(w, req)
	return w
}

func TestCreateComment_IdempotentReplayPreventsDuplicateSpeech(t *testing.T) {
	fixture := createHandlerCommentIssueFixture(t, "comment idempotency "+uuid.NewString())
	key := uuid.NewString()
	content := "one user action " + uuid.NewString()

	first := createCommentWithKey(t, fixture.ID, key, map[string]any{"content": content})
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d %s", first.Code, first.Body.String())
	}
	replay := createCommentWithKey(t, fixture.ID, key, map[string]any{"content": content})
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d %s, want exact %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	var comments int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM comment WHERE issue_id = $1 AND content = $2
	`, fixture.ID, content).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if comments != 1 {
		t.Fatalf("comments = %d, want 1", comments)
	}
	conflict := createCommentWithKey(t, fixture.ID, key, map[string]any{"content": content + " changed"})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed replay = %d %s, want 409", conflict.Code, conflict.Body.String())
	}
}

func TestCreateComment_ConcurrentReplayCreatesOneComment(t *testing.T) {
	fixture := createHandlerCommentIssueFixture(t, "concurrent comment "+uuid.NewString())
	key := uuid.NewString()
	content := "concurrent speech " + uuid.NewString()
	const callers = 8
	responses := make(chan *httptest.ResponseRecorder, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- createCommentWithKey(t, fixture.ID, key, map[string]any{"content": content})
		}()
	}
	wg.Wait()
	close(responses)
	ids := map[string]struct{}{}
	for response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("concurrent create = %d %s", response.Code, response.Body.String())
		}
		var comment CommentResponse
		if err := json.Unmarshal(response.Body.Bytes(), &comment); err != nil {
			t.Fatal(err)
		}
		ids[comment.ID] = struct{}{}
	}
	if len(ids) != 1 {
		t.Fatalf("comment ids = %v, want one", ids)
	}
}

func TestCreateComment_RequestCompletionFailureRollsBackComment(t *testing.T) {
	ctx := context.Background()
	fixture := createHandlerCommentIssueFixture(t, "comment rollback "+uuid.NewString())
	key := uuid.NewString()
	content := "must roll back " + uuid.NewString()
	installResourceCreateCompletionFailure(t, resourceTypeComment, key)
	response := createCommentWithKey(t, fixture.ID, key, map[string]any{"content": content})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("create = %d %s, want 500", response.Code, response.Body.String())
	}
	var comments, requests int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content = $2`, fixture.ID, content).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM resource_create_request WHERE resource_type = 'comment' AND idempotency_key = $1`, key).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if comments != 0 || requests != 0 {
		t.Fatalf("comments=%d requests=%d, want 0/0", comments, requests)
	}
}
