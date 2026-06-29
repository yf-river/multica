package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTaskContextEnvFallbackFeedsCLIAuthAndAttribution(t *testing.T) {
	dir := t.TempDir()
	contextDir := filepath.Join(dir, ".agent_context")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatalf("mkdir context dir: %v", err)
	}
	data := []byte(`{
  "token": "mat_task_from_file",
  "server_url": "http://127.0.0.1:18080",
  "workspace_id": "ws-file",
  "agent_id": "agent-file",
  "task_id": "task-file",
  "daemon_port": "18777"
}`)
	if err := os.WriteFile(filepath.Join(contextDir, "task_env.json"), data, 0o600); err != nil {
		t.Fatalf("write task_env: %v", err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Setenv("MULTICA_TOKEN", "")
	t.Setenv("MULTICA_SERVER_URL", "")
	t.Setenv("MULTICA_WORKSPACE_ID", "")
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")

	cmd := rootCmd
	if got := resolveToken(cmd); got != "mat_task_from_file" {
		t.Fatalf("resolveToken = %q", got)
	}
	if got := resolveWorkspaceID(cmd); got != "ws-file" {
		t.Fatalf("resolveWorkspaceID = %q", got)
	}
	if !inAgentExecutionContext() {
		t.Fatal("expected task_env.json to count as agent execution context")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		t.Fatalf("newAPIClient: %v", err)
	}
	if client.AgentID != "agent-file" || client.TaskID != "task-file" {
		t.Fatalf("client attribution = (%q, %q)", client.AgentID, client.TaskID)
	}
}
