package schema

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	currentVersion  = 1
	advisoryLockKey = int64(7244554146635925501)
	sha256HexLength = sha256.Size * 2
	// legacyLatestMigration is the last migration present when the embedded
	// schema was introduced. A database that recorded this migration has
	// already been upgraded by the old, data-preserving migration runner and
	// can be adopted without replaying the baseline SQL.
	legacyLatestMigration = "525_repository_migration_guard"
)

var (
	errNonEmptyDatabase = errors.New("database is not empty and has no Multica schema marker")
	errVersionMismatch  = errors.New("database schema does not match this server")

	//go:embed current.sql
	currentSQL string

	currentHash = fmt.Sprintf("%x", sha256.Sum256([]byte(currentSQL)))
)

const createMarkerSQL = `
CREATE TABLE public.multica_schema (
    id smallint PRIMARY KEY CHECK (id = 1),
    version integer NOT NULL,
    hash text NOT NULL,
    initialized_at timestamptz NOT NULL DEFAULT now()
);
`

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// EnsureCurrent initializes an empty database with the embedded schema or
// adopts a database that was fully upgraded by the legacy migration runner.
// It never rewrites user tables or data. A non-empty database without a
// recognized migration history, or a version/hash mismatch, remains a hard
// error and must be handled by an explicit migration.
func EnsureCurrent(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("schema initialization requires a database pool")
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin schema initialization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("lock schema initialization: %w", err)
	}

	var markerExists bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass('public.multica_schema') IS NOT NULL").Scan(&markerExists); err != nil {
		return fmt.Errorf("check schema marker: %w", err)
	}
	if markerExists {
		if err := VerifyCurrent(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit schema verification: %w", err)
		}
		return nil
	}

	var hasTables bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_catalog.pg_tables
			WHERE schemaname = 'public'
		)
	`).Scan(&hasTables); err != nil {
		return fmt.Errorf("inspect database tables: %w", err)
	}
	if hasTables {
		adopted, err := adoptLegacySchema(ctx, tx)
		if err != nil {
			return err
		}
		if adopted {
			if err := tx.Commit(ctx); err != nil {
				return fmt.Errorf("commit legacy schema adoption: %w", err)
			}
			return nil
		}
		return errNonEmptyDatabase
	}

	if _, err := tx.Exec(ctx, currentSQL); err != nil {
		return fmt.Errorf("apply current schema: %w", err)
	}
	if _, err := tx.Exec(ctx, createMarkerSQL); err != nil {
		return fmt.Errorf("create schema marker: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.multica_schema (id, version, hash)
		VALUES (1, $1, $2)
	`, currentVersion, currentHash); err != nil {
		return fmt.Errorf("record current schema: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit schema initialization: %w", err)
	}
	return nil
}

// adoptLegacySchema records the marker for a database whose schema was
// completed by the migration runner that preceded the embedded schema. The
// migration table is the durable evidence that every historical migration
// completed; requiring the final migration prevents an arbitrary non-empty
// database from being accepted. This function only creates the bookkeeping
// marker and never changes application tables or rows.
func adoptLegacySchema(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}) (bool, error) {
	var hasMigration bool
	if err := db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'schema_migrations'
		)`).Scan(&hasMigration); err != nil {
		return false, fmt.Errorf("check legacy migration table: %w", err)
	}
	if !hasMigration {
		return false, nil
	}

	var completed bool
	if err := db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM public.schema_migrations WHERE version = $1
		)`, legacyLatestMigration).Scan(&completed); err != nil {
		return false, fmt.Errorf("check legacy migration history: %w", err)
	}
	if !completed {
		return false, nil
	}

	if _, err := db.Exec(ctx, createMarkerSQL); err != nil {
		return false, fmt.Errorf("create schema marker for legacy database: %w", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO public.multica_schema (id, version, hash)
		VALUES (1, $1, $2)
	`, currentVersion, currentHash); err != nil {
		return false, fmt.Errorf("record adopted schema: %w", err)
	}
	return true, nil
}

// VerifyCurrent checks that the database was initialized from the exact schema
// embedded in this server binary.
func VerifyCurrent(ctx context.Context, db rowQuerier) error {
	if db == nil {
		return errors.New("schema verification requires a database")
	}

	var (
		matches bool
		version int
		hash    string
	)
	if err := db.QueryRow(ctx, `
		SELECT version = $1 AND hash = $2, version, hash
		FROM public.multica_schema
		WHERE id = 1
	`, currentVersion, currentHash).Scan(&matches, &version, &hash); err != nil {
		return fmt.Errorf("read schema marker: %w", err)
	}
	if !matches {
		return fmt.Errorf(
			"%w: database version=%d hash=%s, server version=%d hash=%s",
			errVersionMismatch,
			version,
			hash,
			currentVersion,
			currentHash,
		)
	}
	return nil
}
