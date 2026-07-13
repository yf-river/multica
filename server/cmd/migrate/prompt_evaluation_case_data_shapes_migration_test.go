package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestPromptEvaluationCaseDataShapesMigrationNormalizesAndConstrainsChain(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	down := readMigrationFile(t, "095_require_prompt_evaluation_case_data_shapes.down.sql")
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("restore unconstrained case data chain: %v", err)
	}
	var workspaceID, assetID, caseID, datasetRowID, versionID, versionRowID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Case Data Shapes Migration',$1) RETURNING id`, "case-shapes-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,'Case Data Shapes Migration','数据集','{}') RETURNING id`, workspaceID).Scan(&assetID); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_case (workspace_id,asset_id,variables,expected_contains,input,expected,tags) VALUES ($1,$2,'[]','{}','[]','[]','{}') RETURNING id`, workspaceID, assetID).Scan(&caseID); err != nil {
		t.Fatalf("insert invalid case: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_dataset_row (workspace_id,dataset_asset_id,case_id,variables,expected_contains,expected,tags) VALUES ($1,$2,$3,'[]','{}','[]','{}') RETURNING id`, workspaceID, assetID, caseID).Scan(&datasetRowID); err != nil {
		t.Fatalf("insert invalid dataset row: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_dataset_version (workspace_id,dataset_asset_id,version,metadata) VALUES ($1,$2,1,'[]') RETURNING id`, workspaceID, assetID).Scan(&versionID); err != nil {
		t.Fatalf("insert invalid dataset version: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_dataset_version_row (workspace_id,dataset_version_id,dataset_asset_id,variables,expected_contains,expected,tags) VALUES ($1,$2,$3,'[]','{}','[]','{}') RETURNING id`, workspaceID, versionID, assetID).Scan(&versionRowID); err != nil {
		t.Fatalf("insert invalid dataset version row: %v", err)
	}

	up := readMigrationFile(t, "095_require_prompt_evaluation_case_data_shapes.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply case data shape migration attempt %d: %v", attempt+1, err)
		}
	}

	tests := []struct {
		table, field, validType, id string
	}{
		{"prompt_evaluation_case", "variables", "object", caseID},
		{"prompt_evaluation_case", "expected_contains", "array", caseID},
		{"prompt_evaluation_case", "input", "object", caseID},
		{"prompt_evaluation_case", "expected", "object", caseID},
		{"prompt_evaluation_case", "tags", "array", caseID},
		{"prompt_evaluation_dataset_row", "variables", "object", datasetRowID},
		{"prompt_evaluation_dataset_row", "expected_contains", "array", datasetRowID},
		{"prompt_evaluation_dataset_row", "expected", "object", datasetRowID},
		{"prompt_evaluation_dataset_row", "tags", "array", datasetRowID},
		{"prompt_evaluation_dataset_version", "metadata", "object", versionID},
		{"prompt_evaluation_dataset_version_row", "variables", "object", versionRowID},
		{"prompt_evaluation_dataset_version_row", "expected_contains", "array", versionRowID},
		{"prompt_evaluation_dataset_version_row", "expected", "object", versionRowID},
		{"prompt_evaluation_dataset_version_row", "tags", "array", versionRowID},
	}
	for index, test := range tests {
		var actualType string
		query := fmt.Sprintf(`SELECT jsonb_typeof(%s) FROM %s WHERE id=$1`, test.field, test.table)
		if err := tx.QueryRow(ctx, query, test.id).Scan(&actualType); err != nil {
			t.Fatalf("read %s.%s: %v", test.table, test.field, err)
		}
		if actualType != test.validType {
			t.Fatalf("%s.%s type = %s, want %s", test.table, test.field, actualType, test.validType)
		}
		savepoint := fmt.Sprintf("invalid_shape_%d", index)
		if _, err := tx.Exec(ctx, `SAVEPOINT `+savepoint); err != nil {
			t.Fatalf("create savepoint: %v", err)
		}
		invalid := `'null'::jsonb`
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s=%s WHERE id=$1`, test.table, test.field, invalid), test.id); err == nil {
			t.Fatalf("%s.%s constraint accepted null", test.table, test.field)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT `+savepoint); err != nil {
			t.Fatalf("restore rejected shape: %v", err)
		}
	}
}
