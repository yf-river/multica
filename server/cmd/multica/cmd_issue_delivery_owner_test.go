package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func TestDeliveryClaimDoesNotContactServerWhenCredentialsCannotBeStored(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses the read-only proc filesystem")
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".multica")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/proc", filepath.Join(dir, "delivery-owners")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("MULTICA_TASK_ID", "execution")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	cmd := &cobra.Command{}
	cmd.Flags().String("execution", "", "")
	cmd.Flags().String("recovery-evidence", "", "")
	client := &cli.APIClient{BaseURL: server.URL, WorkspaceID: "workspace", HTTPClient: server.Client()}
	if err := deliveryCLIClaim(context.Background(), cmd, client, "/api/issues/issue", "plan"); err == nil {
		t.Fatal("expected an error for a credential directory that cannot be written")
	}
	if requests != 0 {
		t.Fatalf("claimed remote ownership before checking local storage: %d requests", requests)
	}
}
