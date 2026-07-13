package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestProjectResourceContractMigrationRejectsInvalidHistoryAndConstrainsWrites(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		ALTER TABLE project_resource DROP CONSTRAINT IF EXISTS project_resource_type_check;
		ALTER TABLE project_resource DROP CONSTRAINT IF EXISTS project_resource_ref_is_object;
	`); err != nil {
		t.Fatalf("restore unconstrained project resources: %v", err)
	}

	suffix := uuid.NewString()
	var workspaceID, projectID, validID, invalidShapeID, invalidTypeID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Project Resource Migration',$1) RETURNING id`, "project-resource-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO project (workspace_id,title) VALUES ($1,'Project Resource Migration') RETURNING id`, workspaceID).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO project_resource (project_id,workspace_id,resource_type,resource_ref) VALUES ($1,$2,'github_repo','{"url":"https://github.com/multica-ai/multica"}') RETURNING id`, projectID, workspaceID).Scan(&validID); err != nil {
		t.Fatalf("insert valid resource: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO project_resource (project_id,workspace_id,resource_type,resource_ref) VALUES ($1,$2,'github_repo','[]') RETURNING id`, projectID, workspaceID).Scan(&invalidShapeID); err != nil {
		t.Fatalf("insert invalid shape: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO project_resource (project_id,workspace_id,resource_type,resource_ref) VALUES ($1,$2,'retired_repo','{}') RETURNING id`, projectID, workspaceID).Scan(&invalidTypeID); err != nil {
		t.Fatalf("insert invalid type: %v", err)
	}

	up := readMigrationFile(t, "100_require_project_resource_contract.up.sql")
	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_project_resource_history`); err != nil {
		t.Fatalf("create invalid-history savepoint: %v", err)
	}
	if _, err := tx.Exec(ctx, up); err == nil {
		t.Fatal("migration accepted project resources outside the current contract")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_project_resource_history`); err != nil {
		t.Fatalf("restore rejected invalid history: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_resource WHERE id IN ($1,$2)`, invalidShapeID, invalidTypeID); err != nil {
		t.Fatalf("repair invalid fixture rows: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply project resource migration attempt %d: %v", attempt+1, err)
		}
	}
	var validCount int
	if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM project_resource WHERE id=$1`, validID).Scan(&validCount); err != nil {
		t.Fatalf("count valid resource: %v", err)
	}
	if validCount != 1 {
		t.Fatalf("post-migration valid count = %d", validCount)
	}

	for _, insert := range []string{
		`INSERT INTO project_resource (project_id,workspace_id,resource_type,resource_ref) VALUES ($1,$2,'github_repo','[]')`,
		`INSERT INTO project_resource (project_id,workspace_id,resource_type,resource_ref) VALUES ($1,$2,'retired_repo','{}')`,
	} {
		if _, err := tx.Exec(ctx, `SAVEPOINT invalid_project_resource`); err != nil {
			t.Fatalf("create savepoint: %v", err)
		}
		if _, err := tx.Exec(ctx, insert, projectID, workspaceID); err == nil {
			t.Fatalf("project resource constraint accepted %s", insert)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_project_resource`); err != nil {
			t.Fatalf("restore rejected write: %v", err)
		}
	}
}
