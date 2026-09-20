// Package postgres implements internal/app's Repository and Queue ports
// (internal/app/ports.go) on top of a single pgx connection pool, and, in
// this file, embeds the goose migrations that create the schema they read
// and write. It is a leaf implementation: it imports internal/domain only,
// never internal/app -- see CONTRACT.md section 2's one-way import rule --
// so app declares the interfaces this package satisfies structurally.
package postgres

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies every pending migration embedded in migrations/ to the
// database pool points at. It borrows pool's connection string to open a
// short-lived database/sql handle (goose drives migrations through
// database/sql, not pgxpool) and closes that handle before returning; pool
// itself is never closed by Migrate.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDB(*pool.Config().ConnConfig)
	defer db.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("postgres: set goose dialect: %w", err)
	}

	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("postgres: run migrations: %w", err)
	}
	return nil
}
