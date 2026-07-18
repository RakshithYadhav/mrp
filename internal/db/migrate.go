package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx,
	`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)

	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}

	// loops through rows and fills applied map.
	for	rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return err
		}
		applied[v] = true
	}

	// rows.next only returns false 
	// if there is an error you will catch it here.
	if rows.Err() != nil {
		return rows.Err()
	}

	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)

	for _, path := range entries {
		version := path[len("migrations/"):]
		if applied[version] {
			continue
		}

		sql, err := migrationsFS.ReadFile(path)
		if err != nil {
			return err
		}

		transaction, err := pool.Begin(ctx)
		if err != nil {
			return err
		}

		// apply the migrations
		if _, err := transaction.Exec(ctx, string(sql)); err != nil {
			_ = transaction.Rollback(ctx)
			return fmt.Errorf("apply %s : %w", version, err)
		}

		// note down the migration as been completed.
		if _, err := transaction.Exec(ctx,
		`INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = transaction.Rollback(ctx)
			return err
		}

		if err := transaction.Commit(ctx); err != nil {
			return err
		}
		slog.Info("applied migration", "version", version)

	}
	return nil
}