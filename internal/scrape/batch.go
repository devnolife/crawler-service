package scrape

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// maxBatchURLs membatasi jumlah URL per batch.
const maxBatchURLs = 50

// BatchOptions mengatur satu job batch scrape (banyak URL, boleh lintas host).
type BatchOptions struct {
	// URLs daftar target; 1–50 URL.
	URLs []string
	// Delay antar-request (politeness). Default 1s, minimal 500ms.
	Delay time.Duration
	// OnlyMainContent diteruskan ke setiap scrape. Default true di API.
	OnlyMainContent bool
	// OnPage dipanggil per halaman sukses (mis. persist ke DB).
	OnPage func(*Result)
	// RenderJS merender tiap halaman lewat browser CDP (Lightpanda).
	RenderJS bool
	// Renderer dipakai bila RenderJS true. Nil = NewRendererFromEnv().
	Renderer *Renderer
	// Webhook (opsional): URL yang di-POST saat job selesai/gagal.
	Webhook string
	// AllowPrivateHosts hanya untuk testing.
	AllowPrivateHosts bool
}

// StartBatch memvalidasi URL, mendaftarkan job batch, dan menjalankannya async.
func (m *Manager) StartBatch(opts BatchOptions) (*CrawlJob, error) {
	if len(opts.URLs) == 0 {
		return nil, fmt.Errorf("%w: urls kosong", ErrInvalidURL)
	}
	if len(opts.URLs) > maxBatchURLs {
		return nil, fmt.Errorf("maksimal %d url per batch, dapat %d", maxBatchURLs, len(opts.URLs))
	}
	normalized := make([]string, 0, len(opts.URLs))
	for _, raw := range opts.URLs {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%w: %q", ErrInvalidURL, raw)
		}
		normalized = append(normalized, u.String())
	}
	if opts.Delay < 500*time.Millisecond {
		opts.Delay = time.Second
	}
	opts.URLs = normalized

	job := &CrawlJob{
		ID:        newJobID(),
		Kind:      "batch",
		Status:    StatusPending,
		URLs:      normalized,
		CreatedAt: time.Now().UTC(),
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	go m.run(job.ID, opts.Webhook, opts.AllowPrivateHosts, func(ctx context.Context) ([]*Result, []PageError, error) {
		return batchScrape(ctx, opts)
	})
	return snapshot(job), nil
}

// batchScrape men-scrape URL satu per satu dengan delay politeness.
// Tiap URL tetap dicek robots.txt (Scrape default).
func batchScrape(ctx context.Context, opts BatchOptions) ([]*Result, []PageError, error) {
	var pages []*Result
	var pageErrs []PageError

	for i, raw := range opts.URLs {
		if err := ctx.Err(); err != nil {
			return pages, pageErrs, err
		}
		res, err := Scrape(ctx, Options{
			URL:               raw,
			OnlyMainContent:   opts.OnlyMainContent,
			RenderJS:          opts.RenderJS,
			Renderer:          opts.Renderer,
			Timeout:           30 * time.Second,
			AllowPrivateHosts: opts.AllowPrivateHosts,
		})
		if err != nil {
			pageErrs = append(pageErrs, PageError{raw, err.Error()})
		} else {
			pages = append(pages, res)
			if opts.OnPage != nil {
				opts.OnPage(res)
			}
		}

		if i < len(opts.URLs)-1 {
			select {
			case <-ctx.Done():
				return pages, pageErrs, ctx.Err()
			case <-time.After(opts.Delay):
			}
		}
	}
	return pages, pageErrs, nil
}
