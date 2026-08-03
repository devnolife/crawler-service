// Package model berisi tipe data bersama antara crawler dan API.
package model

// Paper adalah satu record hasil crawl repositori akademik.
// Skema identik dengan tabel `papers` di Postgres.
type Paper struct {
	ID           int64    `json:"id"`
	Source       string   `json:"source"`
	SourceID     string   `json:"source_id"`
	Title        string   `json:"title"`
	Authors      []string `json:"authors"`
	Journal      *string  `json:"journal"`
	Year         *int     `json:"year"`
	URL          string   `json:"url"`
	Abstract     *string  `json:"abstract"`
	Keywords     []string `json:"keywords"`
	IsOpenAccess *bool    `json:"is_open_access"`
	HasDataset   *bool    `json:"has_dataset"`
	DatasetURLs  []string `json:"dataset_urls"`
	ScrapedAt    string   `json:"scraped_at"`
}
