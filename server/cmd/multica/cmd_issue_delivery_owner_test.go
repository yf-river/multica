package main

import (
	"context"
	"encoding/json"
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

func TestDeliveryClaimReleasesOwnershipIfSavingTokenFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MULTICA_TASK_ID", "execution")
	dir := filepath.Join(home, ".multica", "delivery-owners")
	var actions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Action     string `json:"action"`
			Token      string `json:"token"`
			Generation int64  `json:"generation"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		actions = append(actions, body.Action)
		if body.Action == "claim" {
			if err := os.Rename(dir, dir+"-original"); err != nil {
				t.Error(err)
			}
			if err := os.WriteFile(dir, []byte("unavailable"), 0600); err != nil {
				t.Error(err)
			}
		} else if body.Action != "release" || body.Token != "owner-token" || body.Generation != 4 {
			t.Errorf("unexpected release: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"owner-token","generation":4}`))
	}))
	defer server.Close()
	cmd := &cobra.Command{}
	cmd.Flags().String("execution", "", "")
	cmd.Flags().String("recovery-evidence", "", "")
	client := &cli.APIClient{BaseURL: server.URL, WorkspaceID: "workspace", HTTPClient: server.Client()}
	if err := deliveryCLIClaim(context.Background(), cmd, client, "/api/issues/issue", "plan"); err == nil {
		t.Fatal("expected token persistence to fail")
	}
	if len(actions) != 2 || actions[0] != "claim" || actions[1] != "release" {
		t.Fatalf("unexpected actions: %v", actions)
	}
}
