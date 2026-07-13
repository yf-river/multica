package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestPromptEvaluationResultObjectsMigrationNormalizesAndConstrainsShapes(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, statement := range []string{
		`ALTER TABLE prompt_evaluation_run DROP CONSTRAINT IF EXISTS prompt_evaluation_run_metrics_is_object, DROP CONSTRAINT IF EXISTS prompt_evaluation_run_evidence_is_object`,
		`ALTER TABLE prompt_evaluation_optimization_candidate DROP CONSTRAINT IF EXISTS prompt_evaluation_optimization_candidate_source_failure_summary_is_object, DROP CONSTRAINT IF EXISTS prompt_evaluation_optimization_candidate_source_prompt_snapshot_is_object, DROP CONSTRAINT IF EXISTS prompt_evaluation_optimization_candidate_metrics_is_object`,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatalf("restore unconstrained result objects: %v", err)
		}
	}
	var workspaceID, promptID, assetID, runID, candidateID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name, slug) VALUES ('Result Objects Migration', $1) RETURNING id`, "result-objects-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_library_item (workspace_id,name,content) VALUES ($1,'Result Objects Migration','Prompt') RETURNING id`, workspaceID).Scan(&promptID); err != nil {
		t.Fatalf("insert prompt: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,prompt_id,name,asset_type,payload) VALUES ($1,$2,'Result Objects Migration','测试套件','{}') RETURNING id`, workspaceID, promptID).Scan(&assetID); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_run (workspace_id,asset_id,prompt_id,run_kind,metrics,evidence) VALUES ($1,$2,$3,'本地渲染','[]','null') RETURNING id`, workspaceID, assetID, promptID).Scan(&runID); err != nil {
		t.Fatalf("insert invalid run result objects: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_optimization_candidate (workspace_id,asset_id,run_id,prompt_id,candidate_name,candidate_content,source_failure_summary,source_prompt_snapshot,metrics) VALUES ($1,$2,$3,$4,'Candidate','Content','[]','null','true') RETURNING id`, workspaceID, assetID, runID, promptID).Scan(&candidateID); err != nil {
		t.Fatalf("insert invalid candidate result objects: %v", err)
	}

	up := readMigrationFile(t, "094_require_prompt_evaluation_result_objects.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply result object migration attempt %d: %v", attempt+1, err)
		}
	}
	var runMetricsType, runEvidenceType string
	if err := tx.QueryRow(ctx, `SELECT jsonb_typeof(metrics), jsonb_typeof(evidence) FROM prompt_evaluation_run WHERE id=$1`, runID).Scan(&runMetricsType, &runEvidenceType); err != nil {
		t.Fatalf("read normalized run objects: %v", err)
	}
	if runMetricsType != "object" || runEvidenceType != "object" {
		t.Fatalf("normalized run types = %s/%s", runMetricsType, runEvidenceType)
	}
	var failureType, snapshotType, metricsType string
	if err := tx.QueryRow(ctx, `SELECT jsonb_typeof(source_failure_summary), jsonb_typeof(source_prompt_snapshot), jsonb_typeof(metrics) FROM prompt_evaluation_optimization_candidate WHERE id=$1`, candidateID).Scan(&failureType, &snapshotType, &metricsType); err != nil {
		t.Fatalf("read normalized candidate objects: %v", err)
	}
	if failureType != "object" || snapshotType != "object" || metricsType != "object" {
		t.Fatalf("normalized candidate types = %s/%s/%s", failureType, snapshotType, metricsType)
	}

	for _, test := range []struct{ table, field, id string }{
		{table: "prompt_evaluation_run", field: "metrics", id: runID},
		{table: "prompt_evaluation_run", field: "evidence", id: runID},
		{table: "prompt_evaluation_optimization_candidate", field: "source_failure_summary", id: candidateID},
		{table: "prompt_evaluation_optimization_candidate", field: "source_prompt_snapshot", id: candidateID},
		{table: "prompt_evaluation_optimization_candidate", field: "metrics", id: candidateID},
	} {
		if _, err := tx.Exec(ctx, `SAVEPOINT invalid_shape`); err != nil {
			t.Fatalf("create invalid-shape savepoint: %v", err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s='[]'::jsonb WHERE id=$1`, test.table, test.field), test.id); err == nil {
			t.Fatalf("%s.%s constraint accepted array", test.table, test.field)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_shape`); err != nil {
			t.Fatalf("restore after %s.%s rejection: %v", test.table, test.field, err)
		}
	}
}
