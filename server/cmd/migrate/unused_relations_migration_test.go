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

	for _, table := range []string{"daemon_connection", "issue_dependency"} {
		if !tableExists(t, ctx, tx, table) {
			t.Fatalf("precondition: %s table does not exist", table)
		}
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
