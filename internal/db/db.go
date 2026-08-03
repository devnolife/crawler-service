// Package db menyediakan koneksi Postgres + skema untuk crawler dan API.
package db

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultURL = "postgresql://postgres:postgres@127.0.0.1:5432/revisi_crawler"

// SchemaSQL adalah satu-satunya sumber kebenaran skema tabel papers.
const SchemaSQL = `
CREATE TABLE IF NOT EXISTS papers (
    id              BIGSERIAL PRIMARY KEY,
    source          TEXT        NOT NULL,
    source_id       TEXT        NOT NULL,
    title           TEXT        NOT NULL,
    authors         TEXT[]      NOT NULL DEFAULT '{}',
    journal         TEXT,
    year            INTEGER,
    url             TEXT        NOT NULL,
    abstract        TEXT,
    keywords        TEXT[]      NOT NULL DEFAULT '{}',
    is_open_access  BOOLEAN,
    has_dataset     BOOLEAN,
    dataset_urls    TEXT[]      NOT NULL DEFAULT '{}',
    scraped_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (source, source_id)
);

CREATE INDEX IF NOT EXISTS papers_year_idx        ON papers (year DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS papers_source_idx      ON papers (source);
CREATE INDEX IF NOT EXISTS papers_scraped_at_idx  ON papers (scraped_at DESC);

-- Full-text search index (simple config; cocok untuk multi-bahasa basic).
CREATE INDEX IF NOT EXISTS papers_fts_idx ON papers
    USING GIN (to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,'')));
`

// URL mengembalikan connection string dari env CRAWLER_DATABASE_URL atau default.
func URL() string {
	if v := os.Getenv("CRAWLER_DATABASE_URL"); v != "" {
		return v
	}
	return defaultURL
}

// Connect membuka connection pool ke database target.
func Connect(ctx context.Context) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, URL())
}

// EnsureSchema membuat database (bila belum ada) lalu menerapkan skema.
func EnsureSchema(ctx context.Context) error {
	url := URL()
	// Pembuatan database harus lewat maintenance DB "postgres".
	idx := strings.LastIndex(url, "/")
	targetDB := url[idx+1:]
	if q := strings.Index(targetDB, "?"); q >= 0 {
		targetDB = targetDB[:q]
	}
	adminURL := url[:idx] + "/postgres"

	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("connect admin db: %w", err)
	}
	defer admin.Close()

	var exists bool
	err = admin.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", targetDB,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check database: %w", err)
	}
	if !exists {
		if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, targetDB)); err != nil {
			return fmt.Errorf("create database: %w", err)
		}
	}

	pool, err := Connect(ctx)
	if err != nil {
		return fmt.Errorf("connect target db: %w", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, SchemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}
