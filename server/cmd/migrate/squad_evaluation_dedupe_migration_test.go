package main

import (
	"context"
	"strings"
	"testing"
)

func TestSquadEvaluationDedupeMigrationAcceptsCurrentSchemaIndex(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	up := readMigrationFile(t, "072_deduplicate_squad_leader_evaluations.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply squad evaluation dedupe migration attempt %d: %v", attempt+1, err)
		}
	}
	var definition string
	if err := tx.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname='public' AND indexname='activity_log_squad_evaluation_task_unique'`).Scan(&definition); err != nil {
		t.Fatalf("read squad evaluation unique index: %v", err)
	}
	if !strings.Contains(definition, "CREATE UNIQUE INDEX") || !strings.Contains(definition, "squad_leader_evaluated") {
		t.Fatalf("unexpected squad evaluation index: %s", definition)
	}
}
