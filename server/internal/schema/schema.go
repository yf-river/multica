package schema

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	currentVersion  = 1
	advisoryLockKey = int64(7244554146635925501)
	sha256HexLength = sha256.Size * 2
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

// EnsureCurrent initializes an empty database with the embedded schema. It
// never upgrades or repairs an existing schema: a non-empty unmarked database
// or a version/hash mismatch is a hard error that requires a database reset.
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
