package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestPromptEvaluationExecutionObjectsMigrationNormalizesAndConstrainsShapes(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, readMigrationFile(t, "096_require_prompt_evaluation_execution_objects.down.sql")); err != nil {
		t.Fatalf("restore unconstrained execution objects: %v", err)
	}

	var workspaceID, assetID, runID, trialID, operationID, snapshotID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Execution Objects Migration',$1) RETURNING id`, "execution-objects-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,'Execution Objects Migration','测试套件','{}') RETURNING id`, workspaceID).Scan(&assetID); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_run (workspace_id,asset_id,run_kind) VALUES ($1,$2,'本地渲染') RETURNING id`, workspaceID, assetID).Scan(&runID); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_trial (run_id,workspace_id,asset_id,input,expected,output,evidence) VALUES ($1,$2,$3,'[]','[]','[]','[]') RETURNING id`, runID, workspaceID, assetID).Scan(&trialID); err != nil {
		t.Fatalf("insert invalid trial: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_case_operation (workspace_id,asset_id,filter,input,sample_case_ids) VALUES ($1,$2,'[]','[]','{}') RETURNING id`, workspaceID, assetID).Scan(&operationID); err != nil {
		t.Fatalf("insert invalid case operation: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_evidence_snapshot (workspace_id,run_id,summary,evidence) VALUES ($1,$2,'[]','[]') RETURNING id`, workspaceID, runID).Scan(&snapshotID); err != nil {
		t.Fatalf("insert invalid evidence snapshot: %v", err)
	}

	up := readMigrationFile(t, "096_require_prompt_evaluation_execution_objects.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply execution object migration attempt %d: %v", attempt+1, err)
		}
	}
	tests := []struct{ table, field, validType, id string }{
		{"prompt_evaluation_trial", "input", "object", trialID},
		{"prompt_evaluation_trial", "expected", "object", trialID},
		{"prompt_evaluation_trial", "evidence", "object", trialID},
		{"prompt_evaluation_case_operation", "filter", "object", operationID},
		{"prompt_evaluation_case_operation", "input", "object", operationID},
		{"prompt_evaluation_case_operation", "sample_case_ids", "array", operationID},
		{"prompt_evaluation_evidence_snapshot", "summary", "object", snapshotID},
		{"prompt_evaluation_evidence_snapshot", "evidence", "object", snapshotID},
	}
	var outputType string
	if err := tx.QueryRow(ctx, `SELECT jsonb_typeof(output) FROM prompt_evaluation_trial WHERE id=$1`, trialID).Scan(&outputType); err != nil {
		t.Fatalf("read open trial output: %v", err)
	}
	if outputType != "array" {
		t.Fatalf("trial output type = %s, want preserved array", outputType)
	}
	for index, test := range tests {
		var actualType string
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT jsonb_typeof(%s) FROM %s WHERE id=$1`, test.field, test.table), test.id).Scan(&actualType); err != nil {
			t.Fatalf("read %s.%s: %v", test.table, test.field, err)
		}
		if actualType != test.validType {
			t.Fatalf("%s.%s type = %s, want %s", test.table, test.field, actualType, test.validType)
		}
		savepoint := fmt.Sprintf("invalid_execution_shape_%d", index)
		if _, err := tx.Exec(ctx, `SAVEPOINT `+savepoint); err != nil {
			t.Fatalf("create savepoint: %v", err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s='null'::jsonb WHERE id=$1`, test.table, test.field), test.id); err == nil {
			t.Fatalf("%s.%s constraint accepted null", test.table, test.field)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT `+savepoint); err != nil {
			t.Fatalf("restore rejected shape: %v", err)
		}
	}
}
