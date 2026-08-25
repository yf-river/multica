package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// These tests lock the current metadata endpoint contract: server and
// transport errors are returned to every command instead of being presented
// as successful metadata operations.

const testIssueUUID = "11111111-1111-1111-1111-111111111111"

func newIssueMetadataListTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "list"}
	c.Flags().String("output", "json", "")
	return c
}

func newIssueMetadataGetTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "get"}
	c.Flags().String("output", "json", "")
	c.Flags().String("key", "", "")
	return c
}

func newIssueMetadataSetTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "set"}
	c.Flags().String("output", "json", "")
	c.Flags().String("key", "", "")
	c.Flags().String("value", "", "")
	c.Flags().String("type", "", "")
	return c
}

func newIssueMetadataDeleteTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "delete"}
	c.Flags().String("output", "json", "")
	c.Flags().String("key", "", "")
	return c
}

// captureStdout used in this file is the (string, error) helper defined
// in cmd_skill_test.go.

// metadataTestServer wires a minimal fake backend that answers the
// resolveIssueRef GET on /api/issues/<id> and forwards every metadata
// request to the supplied handler.
func metadataTestServer(t *testing.T, metadataHandler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/"+testIssueUUID:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         testIssueUUID,
				"identifier": "MUL-1",
				"title":      "test issue",
			})
		case strings.HasPrefix(r.URL.Path, "/api/issues/"+testIssueUUID+"/metadata"):
			metadataHandler(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
}

func TestRunIssueMetadataCommandsReturnErrorsOn404(t *testing.T) {
	tests := []struct {
		name        string
		run         func() error
		errorPrefix string
	}{
		{
			name: "list",
			run: func() error {
				return runIssueMetadataList(newIssueMetadataListTestCmd(), []string{testIssueUUID})
			},
		},
		{
			name: "get",
			run: func() error {
				cmd := newIssueMetadataGetTestCmd()
				_ = cmd.Flags().Set("key", "pr_url")
				return runIssueMetadataGet(cmd, []string{testIssueUUID})
			},
		},
		{
			name: "set",
			run: func() error {
				cmd := newIssueMetadataSetTestCmd()
				_ = cmd.Flags().Set("key", "pr_url")
				_ = cmd.Flags().Set("value", "https://example.com/pr/1")
				return runIssueMetadataSet(cmd, []string{testIssueUUID})
			},
			errorPrefix: "set metadata",
		},
		{
			name: "delete",
			run: func() error {
				cmd := newIssueMetadataDeleteTestCmd()
				_ = cmd.Flags().Set("key", "pr_url")
				return runIssueMetadataDelete(cmd, []string{testIssueUUID})
			},
			errorPrefix: "delete metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadataTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			})

			_, err := captureStdout(t, tt.run)
			var httpErr *cli.HTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
				t.Fatalf("expected 404 *cli.HTTPError, got %v", err)
			}
			if tt.errorPrefix != "" && !strings.Contains(err.Error(), tt.errorPrefix) {
				t.Fatalf("error = %v, want it wrapped with %q prefix", err, tt.errorPrefix)
			}
		})
	}
}

func TestRunIssueMetadataListSuccessReturnsServerMetadata(t *testing.T) {
	metadataTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"metadata": map[string]any{
				"pr_url":          "https://example.com/pr/1",
				"pipeline_status": "waiting_review",
			},
		})
	})

	cmd := newIssueMetadataListTestCmd()
	_ = cmd.Flags().Set("output", "json")

	out, runErr := captureStdout(t, func() error {
		return runIssueMetadataList(cmd, []string{testIssueUUID})
	})
	if runErr != nil {
		t.Fatalf("runIssueMetadataList: %v", runErr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode stdout JSON: %v\n%s", err, out)
	}
	if got["pr_url"] != "https://example.com/pr/1" || got["pipeline_status"] != "waiting_review" {
		t.Fatalf("stdout = %#v, missing expected keys", got)
	}
}

func TestRunIssueMetadataListPropagatesNon404Error(t *testing.T) {
	metadataTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	cmd := newIssueMetadataListTestCmd()
	_ = cmd.Flags().Set("output", "json")

	// Drop stdout to keep test output clean even if the implementation
	// regresses and prints something.
	_, err := captureStdout(t, func() error {
		return runIssueMetadataList(cmd, []string{testIssueUUID})
	})
	if err == nil {
		t.Fatal("runIssueMetadataList returned nil on 500, want error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %v, want it to mention status 500", err)
	}
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error chain missing *cli.HTTPError: %v", err)
	}
	if httpErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("HTTPError.StatusCode = %d, want 500", httpErr.StatusCode)
	}
}

func TestRunIssueMetadataListPropagates401Error(t *testing.T) {
	metadataTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})

	cmd := newIssueMetadataListTestCmd()
	_ = cmd.Flags().Set("output", "json")

	_, err := captureStdout(t, func() error {
		return runIssueMetadataList(cmd, []string{testIssueUUID})
	})
	if err == nil {
		t.Fatal("runIssueMetadataList returned nil on 401, want error")
	}
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 *cli.HTTPError, got %v", err)
	}
}
