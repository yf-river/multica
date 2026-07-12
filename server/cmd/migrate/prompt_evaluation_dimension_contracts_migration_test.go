package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPromptEvaluationDimensionContractsMigrationSeparatesMetricsAndScores(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var workspaceID, assetID, mappedAssetID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Dimension Contracts Migration',$1) RETURNING id`, "dimension-contracts-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	payload := `{"custom":"preserved","metric_contract":["pass_rate"],"指标口径":"仅统计当前快照","实验维度":[{"维度":"命中率","weight":2},"中文一致性"]}`
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,'Dimension Contracts','测试套件',$2) RETURNING id`, workspaceID, payload).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	mappedPayload := `{"评估维度":{"质量":{"weight":3},"安全":"required"}}`
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,'Mapped Dimensions','测试套件',$2) RETURNING id`, workspaceID, mappedPayload).Scan(&mappedAssetID); err != nil {
		t.Fatal(err)
	}
	migration := readMigrationFile(t, "046_separate_prompt_evaluation_dimension_contracts.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply attempt %d: %v", attempt+1, err)
		}
	}
	var metric, note, firstName, secondName, custom string
	var hasAliases bool
	if err := tx.QueryRow(ctx, `
		SELECT payload->'metric_contract'->>0, payload->'metric_notes'->>0,
		       payload->'experiment_dimensions'->0->>'name',
		       payload->'experiment_dimensions'->>1, payload->>'custom',
		       payload ?| ARRAY['指标口径','对比维度','实验维度','evaluation_dimensions','评估维度','指标']
		FROM prompt_evaluation_asset WHERE id=$1
	`, assetID).Scan(&metric, &note, &firstName, &secondName, &custom, &hasAliases); err != nil {
		t.Fatal(err)
	}
	if metric != "pass_rate" || note != "仅统计当前快照" || firstName != "命中率" || secondName != "中文一致性" || custom != "preserved" || hasAliases {
		t.Fatalf("metric=%q note=%q first=%q second=%q custom=%q aliases=%v", metric, note, firstName, secondName, custom, hasAliases)
	}
	var mappedFirst, mappedFirstValue, mappedSecond string
	var mappedWeight int
	if err := tx.QueryRow(ctx, `
		SELECT payload->'experiment_dimensions'->0->>'name',
		       payload->'experiment_dimensions'->0->>'value',
		       payload->'experiment_dimensions'->1->>'name',
		       (payload->'experiment_dimensions'->1->>'weight')::int
		FROM prompt_evaluation_asset WHERE id=$1
	`, mappedAssetID).Scan(&mappedFirst, &mappedFirstValue, &mappedSecond, &mappedWeight); err != nil {
		t.Fatal(err)
	}
	if mappedFirst != "安全" || mappedFirstValue != "required" || mappedSecond != "质量" || mappedWeight != 3 {
		t.Fatalf("mapped first=%q value=%q second=%q weight=%d", mappedFirst, mappedFirstValue, mappedSecond, mappedWeight)
	}
	invalidPayloads := []string{
		`{"metric_contract":{}}`,
		`{"metric_notes":"not-an-array"}`,
		`{"experiment_dimensions":{}}`,
	}
	for index, invalidPayload := range invalidPayloads {
		if _, err := tx.Exec(ctx, `SAVEPOINT invalid_dimension_contract`); err != nil {
			t.Fatal(err)
		}
		_, insertErr := tx.Exec(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,$2,'测试套件',$3)`, workspaceID, "Invalid Dimension Contract", invalidPayload)
		if insertErr == nil {
			t.Fatalf("invalid payload %d was accepted", index)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_dimension_contract`); err != nil {
			t.Fatal(err)
		}
	}
}
