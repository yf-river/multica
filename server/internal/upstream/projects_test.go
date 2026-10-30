package upstream

import (
	"context"
	"testing"
)

func TestProjectQueryBindsIdentityWithoutInterpolatingValues(t *testing.T) {
	sql, args := projectQuery("x'; DROP TABLE agent; --", `SELECT * FROM upstream_sync_item WHERE project_id=@project AND status=$1 OR (project_id=@project AND id=$2)`, []any{"open", "id"})
	if sql != `SELECT * FROM upstream_sync_item WHERE project_id=$3 AND status=$1 OR (project_id=$3 AND id=$2)` || len(args) != 3 || args[2] != "x'; DROP TABLE agent; --" {
		t.Fatalf("unexpected binding: %q %#v", sql, args)
	}
}

func TestProjectHistoriesRemainIndependent(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	for _, id := range []string{"multica", "openviking"} {
		project := s.ForProject(id)
		if err := s.SaveProject(ctx, Project{ID: id, Name: id, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := project.Snapshot(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := project.exec(ctx, `INSERT INTO upstream_sync_item(project_id,id,run_id,cluster_key,title) VALUES(@project,$1,$2,'same-topic',$3)`, newID(), newID(), id); err != nil {
			t.Fatal(err)
		}
	}
	multica, _, err := s.ListItems(ctx, ItemFilter{})
	if err != nil || len(multica) != 1 || multica[0].Title != "multica" {
		t.Fatalf("multica %#v %v", multica, err)
	}
	ov := s.ForProject("openviking")
	items, _, err := ov.ListItems(ctx, ItemFilter{})
	if err != nil || len(items) != 1 || items[0].Title != "openviking" {
		t.Fatalf("OpenViking %#v %v", items, err)
	}
	n, err := ov.MarkItems(ctx, []string{multica[0].ID}, "ignored", newID())
	if err != nil || n != 0 {
		t.Fatalf("cross-project mark: %d %v", n, err)
	}
	projects, err := s.Projects(ctx)
	if err != nil || len(projects) != 2 {
		t.Fatalf("project snapshots %#v %v", projects, err)
	}
}

func TestProjectsConfigureExecutorsInSameWorkspace(t *testing.T) {
	s := isolatedService(t)
	ctx := context.Background()
	ws, owner, runtime := newID(), newID(), newID()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO agent_runtime(id,workspace_id,name,runtime_mode,provider) VALUES($1,$2,'test','local','codex')`, runtime, ws); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, project := range []string{"multica", "openviking", "other"} {
		id, _, err := s.ForProject(project).SaveSettings(ctx, Settings{RuntimeID: runtime, UpstreamURL: "https://example.invalid/" + project + ".git", UpstreamBranch: "main", LocalRef: "origin/main", BatchSize: 1, Enabled: true}, owner)
		if err != nil {
			t.Fatalf("%s: %v", project, err)
		}
		if seen[id] {
			t.Fatal("projects reused an executor")
		}
		seen[id] = true
	}
	var schedules int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM autopilot_trigger WHERE kind='schedule' AND enabled`).Scan(&schedules); err != nil || schedules != 0 {
		t.Fatalf("schedules=%d err=%v", schedules, err)
	}
}
