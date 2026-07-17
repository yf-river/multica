package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestRelativeWorkDir(t *testing.T) {
	const (
		wsID   = "a05b0e10-ee7a-4603-a72d-a548b2390cb2"
		taskID = "5c57b65b-ee7a-4603-a72d-a548b2390cb2"
	)

	tests := []struct {
		name, workDir, wsID, taskID, expected string
	}{
		{"empty work_dir returns empty", "", wsID, taskID, ""},
		{"standard envRoot path strips workspaces root", "/Users/alice/multica_workspaces/" + wsID + "/5c57b65b/workdir", wsID, taskID, wsID + "/5c57b65b/workdir"},
		{"standard envRoot path without trailing workdir", "/Users/alice/multica_workspaces/" + wsID + "/5c57b65b", wsID, taskID, wsID + "/5c57b65b"},
		{"local_directory path under /Users home is stripped", "/Users/df007df/repos/foo", wsID, taskID, "repos/foo"},
		{"local_directory deep path under home keeps full remainder", "/Users/df007df/code/work/projects/multica/foo", wsID, taskID, "code/work/projects/multica/foo"},
		{"shallow /Users home path strips username segment", "/Users/alice/foo", wsID, taskID, "foo"},
		{"shallow Linux /home path strips username segment", "/home/alice/project", wsID, taskID, "project"},
		{"shallow Windows /Users path strips username segment", `C:\Users\alice\foo`, wsID, taskID, "foo"},
		{"exact home directory returns empty", "/Users/alice", wsID, taskID, ""},
		{"exact home directory with trailing slash returns empty", "/Users/alice/", wsID, taskID, ""},
		{"Windows local_directory path under home strips username", `C:\Users\alice\repos\foo`, wsID, taskID, "repos/foo"},
		{"non-home local path falls back to basename only", "/opt/foo", wsID, taskID, "foo"},
		{"non-home deep local path falls back to basename only", "/srv/git/repo", wsID, taskID, "repo"},
		{"single-segment local path returns the segment", "/foo", wsID, taskID, "foo"},
		{"Windows backslash separators are normalized", `C:\Users\alice\multica_workspaces\` + wsID + `\5c57b65b\workdir`, wsID, taskID, wsID + "/5c57b65b/workdir"},
		{"missing workspace_id strips home prefix", "/Users/alice/multica_workspaces/" + wsID + "/5c57b65b/workdir", "", taskID, "multica_workspaces/" + wsID + "/5c57b65b/workdir"},
		{"missing task_id strips home prefix", "/Users/alice/multica_workspaces/" + wsID + "/5c57b65b/workdir", wsID, "", "multica_workspaces/" + wsID + "/5c57b65b/workdir"},
		{"trailing slash is preserved", "/Users/alice/multica_workspaces/" + wsID + "/5c57b65b/workdir/", wsID, taskID, wsID + "/5c57b65b/workdir/"},
		{"workspace ID elsewhere falls back to basename", "/var/" + wsID + "/something/else", wsID, taskID, "else"},
		{"lowercase /users matches /Users", "/users/alice/repos/foo", wsID, taskID, "repos/foo"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := relativeWorkDir(tc.workDir, tc.wsID, tc.taskID)
			if got != tc.expected {
				t.Fatalf("relativeWorkDir(%q, %q, %q) = %q, want %q",
					tc.workDir, tc.wsID, tc.taskID, got, tc.expected)
			}
		})
	}
}

func TestShortTaskIDMatchesDaemon(t *testing.T) {
	const (
		workspacesRoot = "/tmp/workspaces"
		workspaceID    = "a05b0e10-ee7a-4603-a72d-a548b2390cb2"
		taskID         = "5c57b65b-ee7a-4603-a72d-a548b2390cb2"
	)
	daemonRoot := execenv.PredictRootDir(workspacesRoot, workspaceID, taskID)
	expected := workspacesRoot + "/" + workspaceID + "/" + shortTaskID(taskID)
	if daemonRoot != expected {
		t.Fatalf("daemon PredictRootDir = %q, handler-side reconstruction = %q — shortTaskID is out of sync with execenv.shortID", daemonRoot, expected)
	}
}
