package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/devnolife/crawler-service/internal/model"
)

const upsertSQL = `
INSERT INTO papers (
    source, source_id, title, authors, journal, year, url, abstract,
    keywords, is_open_access, has_dataset, dataset_urls, scraped_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW()
)
ON CONFLICT (source, source_id) DO UPDATE SET
    title          = EXCLUDED.title,
    authors        = EXCLUDED.authors,
    journal        = EXCLUDED.journal,
    year           = EXCLUDED.year,
    url            = EXCLUDED.url,
    abstract       = EXCLUDED.abstract,
    keywords       = EXCLUDED.keywords,
    is_open_access = EXCLUDED.is_open_access,
    has_dataset    = EXCLUDED.has_dataset,
    dataset_urls   = EXCLUDED.dataset_urls,
    scraped_at     = NOW();
`

// UpsertPaper menyimpan/memperbarui satu paper (idempotent by source+source_id).
func UpsertPaper(ctx context.Context, pool *pgxpool.Pool, p model.Paper) error {
	_, err := pool.Exec(ctx, upsertSQL,
		p.Source, p.SourceID, p.Title, p.Authors, p.Journal, p.Year,
		p.URL, p.Abstract, p.Keywords, p.IsOpenAccess, p.HasDataset, p.DatasetURLs,
	)
	return err
}
