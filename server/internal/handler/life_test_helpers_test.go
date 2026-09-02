package handler

import (
	"context"
	"net/http"
	"testing"
)

type errorRow struct{ err error }

func (r errorRow) Scan(...interface{}) error { return r.err }

func requireHandlerDatabase(t *testing.T) {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
}

func mustExec(t testing.TB, ctx context.Context, query string, args ...any) {
	t.Helper()
	if _, err := testPool.Exec(ctx, query, args...); err != nil {
		t.Fatalf("execute test SQL: %v", err)
	}
}

func setTaskTokenActor(req *http.Request, agentID, taskID string) {
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
}
