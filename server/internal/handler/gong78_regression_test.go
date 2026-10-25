package handler

import (
	"context"
	"github.com/multica-ai/multica/server/internal/testutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAuditRound2ClipKeepsValidUTF8(t *testing.T) {
	if !utf8.ValidString(clipText(strings.Repeat("中", 30000))) {
		t.Fatal("64KiB byte clip splits UTF-8 code point")
	}
}
func TestAuditRound2LongChineseTextDoesNotAbortEdit(t *testing.T) {
	agent := dbfx.Agent(t, "audit long Chinese", testRuntimeID)
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": testRuntimeID, "status": "running"})
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	q := testHandler.Queries.WithTx(tx)
	if _, err = tx.Exec(context.Background(), `UPDATE agent SET description='audit changed' WHERE id=$1`, agent); err != nil {
		t.Fatal(err)
	}
	req := newRequest(http.MethodPut, "/audit", nil)
	req.Header.Set("X-Task-ID", task)
	testHandler.recordRunTextChange(context.Background(), q, req, testWorkspaceID, runTextTarget{Kind: "agent", ID: agent, Name: "audit"}, "instructions", "old", strings.Repeat("中", 30000))
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatalf("best-effort audit aborted main edit transaction: %v", err)
	}
}

func TestAuditRound2APIUpdateLargeChinesePrompt(t *testing.T) {
	agent := dbfx.Agent(t, "audit API Chinese", testRuntimeID)
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": testRuntimeID, "status": "running"})
	req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agent, map[string]any{"instructions": strings.Repeat("中", 30000)}), "id", agent)
	req.Header.Set("X-Task-ID", task)
	rec := httptest.NewRecorder()
	testHandler.UpdateAgent(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid 90KB Chinese prompt update returned HTTP %d: %s", rec.Code, rec.Body.String())
	}
}
