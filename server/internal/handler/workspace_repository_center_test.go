package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestRepositoryCenterIncludesDurableDirectory(t *testing.T) {
	projectID := dbfx.Insert(t, "project", testutil.Cols{"workspace_id": testWorkspaceID, "title": "Repository directory audit"})
	issueID := dbfx.Issue(t, "Repository durable directory", testutil.Cols{"project_id": projectID, "priority": "not_important_not_urgent"})
	runtimeID := dbfx.Runtime(t, "Repository audit runtime")
	agentID := dbfx.Agent(t, "Repository audit agent", runtimeID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{"issue_id": issueID, "runtime_id": runtimeID, "status": "completed", "work_dir": "", "durable_work_dir": "/workspace/retained-project"})
	req := withURLParam(newRequest("GET", "/api/workspaces/"+testWorkspaceID+"/repos/details", nil), "id", testWorkspaceID)
	var response struct {
		Locations []repositoryLocation `json:"locations"`
	}
	testutil.Call(t, testHandler.GetWorkspaceRepositoryCenter, req).Want(200).JSON(&response)
	for _, location := range response.Locations {
		if location.TaskID == taskID {
			if location.WorkDir != "/workspace/retained-project" {
				t.Fatalf("directory = %q", location.WorkDir)
			}
			return
		}
	}
	t.Fatal("task with a durable directory is missing from repository center")
}
