package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPromptEvaluationDatasetLinksMigrationNormalizesAliases(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var workspaceID, assetID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Dataset Links Migration',$1) RETURNING id`, "dataset-links-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	payload := `{"custom":"preserved","数据集版本":[{"version_id":"v1","dataset_id":"d1","名称":"Dataset","version":3,"行指纹":"fp"}],"关联数据集ID":"d2"}`
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,'Dataset Links','测试套件',$2) RETURNING id`, workspaceID, payload).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	migration := readMigrationFile(t, "045_normalize_prompt_evaluation_dataset_links.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply attempt %d: %v", attempt+1, err)
		}
	}
	var versionID, datasetID, datasetName, custom, linkedID string
	var hasOldTop, hasOldNested bool
	if err := tx.QueryRow(ctx, `
		SELECT payload->'linked_dataset_versions'->0->>'dataset_version_id',
		       payload->'linked_dataset_versions'->0->>'dataset_asset_id',
		       payload->'linked_dataset_versions'->0->>'dataset_name',
		       payload->>'custom', payload->'linked_dataset_ids'->>0,
		       payload ?| ARRAY['数据集版本','关联数据集版本','dataset_ids','数据集ID','关联数据集ID'],
		       (payload->'linked_dataset_versions'->0) ?| ARRAY['version_id','数据集版本ID','dataset_id','数据集ID','name','名称','数据集名称']
		FROM prompt_evaluation_asset WHERE id=$1
	`, assetID).Scan(&versionID, &datasetID, &datasetName, &custom, &linkedID, &hasOldTop, &hasOldNested); err != nil {
		t.Fatal(err)
	}
	if versionID != "v1" || datasetID != "d1" || datasetName != "Dataset" || custom != "preserved" || linkedID != "d2" || hasOldTop || hasOldNested {
		t.Fatalf("normalized values version=%q dataset=%q name=%q custom=%q linked=%q oldTop=%v oldNested=%v", versionID, datasetID, datasetName, custom, linkedID, hasOldTop, hasOldNested)
	}
}
