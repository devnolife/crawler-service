package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/devnolife/crawler-service/internal/scrape"
)

// PagesSchemaSQL adalah skema tabel scraped_pages (hasil crawl on-demand
// yang di-persist). Idempotent — aman dijalankan berulang.
const PagesSchemaSQL = `
CREATE TABLE IF NOT EXISTS scraped_pages (
    url          TEXT        PRIMARY KEY,
    host         TEXT        NOT NULL,
    title        TEXT,
    description  TEXT,
    language     TEXT,
    markdown     TEXT        NOT NULL,
    status_code  INTEGER     NOT NULL,
    scraped_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS scraped_pages_host_idx       ON scraped_pages (host);
CREATE INDEX IF NOT EXISTS scraped_pages_scraped_at_idx ON scraped_pages (scraped_at DESC);

CREATE INDEX IF NOT EXISTS scraped_pages_fts_idx ON scraped_pages
    USING GIN (to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(markdown,'')));
`

const upsertPageSQL = `
INSERT INTO scraped_pages (url, host, title, description, language, markdown, status_code, scraped_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (url) DO UPDATE SET
    title       = EXCLUDED.title,
    description = EXCLUDED.description,
    language    = EXCLUDED.language,
    markdown    = EXCLUDED.markdown,
    status_code = EXCLUDED.status_code,
    scraped_at  = EXCLUDED.scraped_at;
`

// EnsurePagesSchema menerapkan skema scraped_pages pada pool yang ada.
func EnsurePagesSchema(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, PagesSchemaSQL)
	return err
}

// UpsertPage menyimpan satu hasil scrape (idempotent by url).
func UpsertPage(ctx context.Context, pool *pgxpool.Pool, host string, r *scrape.Result) error {
	_, err := pool.Exec(ctx, upsertPageSQL,
		r.URL, host, r.Title, r.Description, r.Language,
		r.Markdown, r.StatusCode, r.ScrapedAt,
	)
	return err
}
