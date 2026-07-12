package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPromptEvaluationExperimentContextMigrationNormalizesAliases(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var workspaceID, assetID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Experiment Context Migration',$1) RETURNING id`, "experiment-context-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	payload := `{"custom":"preserved","实验对象":"current prompt","baseline_result":42}`
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,'Experiment Context','测试套件',$2) RETURNING id`, workspaceID, payload).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	migration := readMigrationFile(t, "047_normalize_prompt_evaluation_experiment_context.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply attempt %d: %v", attempt+1, err)
		}
	}
	var target, baseline, custom string
	var hasAliases bool
	if err := tx.QueryRow(ctx, `
		SELECT payload->>'experiment_target', payload->>'baseline_output', payload->>'custom',
		       payload ?| ARRAY['实验对象','target','对象','基线输出','baseline','baseline_result']
		FROM prompt_evaluation_asset WHERE id=$1
	`, assetID).Scan(&target, &baseline, &custom, &hasAliases); err != nil {
		t.Fatal(err)
	}
	if target != "current prompt" || baseline != "42" || custom != "preserved" || hasAliases {
		t.Fatalf("target=%q baseline=%q custom=%q aliases=%v", target, baseline, custom, hasAliases)
	}
	for index, invalidPayload := range []string{`{"experiment_target":{}}`, `{"baseline_output":[]}`} {
		if _, err := tx.Exec(ctx, `SAVEPOINT invalid_experiment_context`); err != nil {
			t.Fatal(err)
		}
		_, insertErr := tx.Exec(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,$2,'测试套件',$3)`, workspaceID, "Invalid Experiment Context", invalidPayload)
		if insertErr == nil {
			t.Fatalf("invalid payload %d was accepted", index)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_experiment_context`); err != nil {
			t.Fatal(err)
		}
	}
}
