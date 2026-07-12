package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

func newIssueRerunTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "rerun"}
	cmd.Flags().String("output", "json", "")
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	return cmd
}

func TestRunIssueRerunRetriesWithOneRequestIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-123")
	const issueID = "44444444-4444-4444-8444-444444444444"
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/issues/"+issueID {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": issueID, "identifier": "MUL-1"})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/issues/"+issueID+"/rerun" {
			t.Fatalf("request = %s %q", r.Method, r.URL.Path)
		}
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.Header().Set("Content-Type", "application/json")
		if len(keys) == 1 {
			_, _ = w.Write([]byte(`{"id":`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "task-1", "agent_id": "agent-1", "status": "queued",
		})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := newIssueRerunTestCmd()
	if _, err := captureStdout(t, func() error { return runIssueRerun(cmd, []string{issueID}) }); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || len(keys[0]) != 36 || keys[1] != keys[0] {
		t.Fatalf("issue rerun keys = %#v, want two matching UUIDs", keys)
	}
}
