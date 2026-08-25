package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func resolveCommentHTTP(t *testing.T, commentID string) {
	t.Helper()
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/comments/"+commentID+"/resolve", nil)
	r = withURLParam(r, "commentId", commentID)
	testHandler.ResolveComment(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve %s: status %d: %s", commentID, w.Code, w.Body.String())
	}
	var resp CommentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
}

func commentResolved(t *testing.T, id string) bool {
	t.Helper()
	return commentResolvedAt(t, id) != nil
}

func commentResolvedAt(t *testing.T, id string) *time.Time {
	t.Helper()
	var resolvedAt *time.Time
	if err := testPool.QueryRow(context.Background(),
		`SELECT resolved_at FROM comment WHERE id = $1`, id,
	).Scan(&resolvedAt); err != nil {
		t.Fatalf("query resolved_at for %s: %v", id, err)
	}
	return resolvedAt
}

type commentEventCapture struct {
	mu     sync.Mutex
	events []struct {
		Type      string
		CommentID string
	}
}

func captureCommentEvents(t *testing.T, issueID string) *commentEventCapture {
	t.Helper()
	cap := &commentEventCapture{}
	record := func(e events.Event) {
		m, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		c, ok := m["comment"].(CommentResponse)
		if !ok || c.IssueID != issueID {
			return
		}
		cap.mu.Lock()
		cap.events = append(cap.events, struct {
			Type      string
			CommentID string
		}{e.Type, c.ID})
		cap.mu.Unlock()
	}
	testHandler.Bus.Subscribe(protocol.EventCommentResolved, record)
	testHandler.Bus.Subscribe(protocol.EventCommentUnresolved, record)
	return cap
}

func (c *commentEventCapture) countFor(eventType, commentID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e.Type == eventType && e.CommentID == commentID {
			n++
		}
	}
	return n
}

type resolveTestFixture struct {
	IssueID string
	Root1   string
	A1      string
	B1      string
	Root2   string
	A2      string
}

func newResolveTestFixture(t *testing.T) resolveTestFixture {
	t.Helper()

	seed := newSeededCommentIssue(t, "resolve fixture")
	root1 := seed.insertComment(t, nil, 0, "root1")
	a1 := seed.insertComment(t, &root1, 1*time.Minute, "a1")
	b1 := seed.insertComment(t, &root1, 2*time.Minute, "b1")
	root2 := seed.insertComment(t, nil, 10*time.Minute, "root2")
	a2 := seed.insertComment(t, &root2, 11*time.Minute, "a2")

	return resolveTestFixture{IssueID: seed.IssueID, Root1: root1, A1: a1, B1: b1, Root2: root2, A2: a2}
}

func TestResolveComment_ReplacesPriorThreadResolution(t *testing.T) {
	requireHandlerDatabase(t)
	fx := newResolveTestFixture(t)
	cap := captureCommentEvents(t, fx.IssueID)

	resolveCommentHTTP(t, fx.A1)
	if !commentResolved(t, fx.A1) {
		t.Fatalf("a1 should be resolved after first resolve")
	}

	resolveCommentHTTP(t, fx.B1)
	if !commentResolved(t, fx.B1) {
		t.Fatalf("b1 should be resolved")
	}
	if commentResolved(t, fx.A1) {
		t.Fatalf("a1 should have been cleared when b1 was resolved (single-resolution invariant)")
	}

	if got := cap.countFor(protocol.EventCommentUnresolved, fx.A1); got != 1 {
		t.Fatalf("expected exactly 1 comment:unresolved for a1, got %d", got)
	}
	if got := cap.countFor(protocol.EventCommentResolved, fx.B1); got != 1 {
		t.Fatalf("expected exactly 1 comment:resolved for b1, got %d", got)
	}
}

func TestResolveComment_ScopedToThread(t *testing.T) {
	requireHandlerDatabase(t)
	fx := newResolveTestFixture(t)

	resolveCommentHTTP(t, fx.B1)
	resolveCommentHTTP(t, fx.A2)
	if !commentResolved(t, fx.B1) {
		t.Fatalf("b1 (thread 1) must stay resolved when a separate thread is resolved")
	}
	if !commentResolved(t, fx.A2) {
		t.Fatalf("a2 (thread 2) should be resolved")
	}

	resolveCommentHTTP(t, fx.Root1)
	if !commentResolved(t, fx.Root1) {
		t.Fatalf("root1 should be resolved")
	}
	if commentResolved(t, fx.B1) {
		t.Fatalf("b1 should be cleared when root1 becomes the resolution")
	}
	if !commentResolved(t, fx.A2) {
		t.Fatalf("a2 (other thread) must remain resolved throughout")
	}
}

func TestResolveComment_ReResolveIsIdempotent(t *testing.T) {
	requireHandlerDatabase(t)
	fx := newResolveTestFixture(t)
	cap := captureCommentEvents(t, fx.IssueID)

	resolveCommentHTTP(t, fx.A1)
	first := commentResolvedAt(t, fx.A1)
	if first == nil {
		t.Fatalf("a1 should be resolved")
	}

	resolveCommentHTTP(t, fx.A1)
	second := commentResolvedAt(t, fx.A1)
	if second == nil || !second.Equal(*first) {
		t.Fatalf("re-resolve must keep the original resolved_at (got %v, want %v)", second, first)
	}
	if got := cap.countFor(protocol.EventCommentResolved, fx.A1); got != 1 {
		t.Fatalf("re-resolve no-op must not emit a second comment:resolved (got %d)", got)
	}
}
