package main

import (
	"context"
	"testing"
)

func TestUnusedRelationsMigrationRemovesDeadTables(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS daemon_connection (
			id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
			agent_id uuid NOT NULL,
			daemon_id text NOT NULL
		);
		CREATE TABLE IF NOT EXISTS issue_dependency (
			id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
			issue_id uuid NOT NULL,
			depends_on_issue_id uuid NOT NULL,
			type text NOT NULL
		);
	`); err != nil {
		t.Fatalf("restore removed relation shape: %v", err)
	}

	up := readMigrationFile(t, "030_remove_unused_relations.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply migration attempt %d: %v", attempt+1, err)
		}
	}

	for _, table := range []string{"daemon_connection", "issue_dependency"} {
		if tableExists(t, ctx, tx, table) {
			t.Fatalf("%s table still exists after migration", table)
		}
	}
}
