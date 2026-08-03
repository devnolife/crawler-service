package scrape

import (
	"fmt"
	"time"
)

// JobRequest adalah permintaan job yang serializable (JSON) sehingga bisa
// masuk queue Redis maupun dieksekusi langsung in-memory.
type JobRequest struct {
	Kind            string   `json:"kind"` // "crawl" | "batch"
	URL             string   `json:"url,omitempty"`
	URLs            []string `json:"urls,omitempty"`
	MaxPages        int      `json:"max_pages,omitempty"`
	MaxDepth        int      `json:"max_depth,omitempty"`
	DelayMS         int      `json:"delay_ms,omitempty"`
	OnlyMainContent bool     `json:"only_main_content"`
	Persist         bool     `json:"persist,omitempty"`
	RenderJS        bool     `json:"render_js,omitempty"`
	Webhook         string   `json:"webhook,omitempty"`
	// AllowPrivateHosts hanya untuk testing. Ikut serialisasi payload queue
	// (internal, bukan input user API) supaya worker Redis menghormatinya.
	AllowPrivateHosts bool `json:"allow_private,omitempty"`
}

// Deps adalah dependensi runtime yang tidak ikut serialisasi.
type Deps struct {
	// Renderer untuk render_js. Nil = NewRendererFromEnv().
	Renderer *Renderer
	// PersistPage dipanggil per halaman sukses bila request.Persist true.
	PersistPage func(*Result)
}

// Runner menjalankan job scrape/crawl async. Implementasi: Manager
// (in-memory) dan RedisRunner (durable, queue asynq).
type Runner interface {
	// Enqueue memvalidasi request dan menjadwalkan job; return snapshot awal.
	Enqueue(req JobRequest) (*CrawlJob, error)
	// Get mengembalikan snapshot job, nil bila tidak dikenal/kedaluwarsa.
	Get(id string) *CrawlJob
}

// toCrawlOptions mengonversi request kind=crawl ke opsi eksekusi.
func (r JobRequest) toCrawlOptions(deps Deps) CrawlOptions {
	opts := CrawlOptions{
		URL:               r.URL,
		MaxPages:          r.MaxPages,
		MaxDepth:          r.MaxDepth,
		Delay:             time.Duration(r.DelayMS) * time.Millisecond,
		OnlyMainContent:   r.OnlyMainContent,
		RenderJS:          r.RenderJS,
		Renderer:          deps.Renderer,
		Webhook:           r.Webhook,
		AllowPrivateHosts: r.AllowPrivateHosts,
	}
	if r.Persist {
		opts.OnPage = deps.PersistPage
	}
	return opts
}

// toBatchOptions mengonversi request kind=batch ke opsi eksekusi.
func (r JobRequest) toBatchOptions(deps Deps) BatchOptions {
	opts := BatchOptions{
		URLs:              r.URLs,
		Delay:             time.Duration(r.DelayMS) * time.Millisecond,
		OnlyMainContent:   r.OnlyMainContent,
		RenderJS:          r.RenderJS,
		Renderer:          deps.Renderer,
		Webhook:           r.Webhook,
		AllowPrivateHosts: r.AllowPrivateHosts,
	}
	if r.Persist {
		opts.OnPage = deps.PersistPage
	}
	return opts
}

// InMemoryRunner membungkus Manager agar memenuhi interface Runner.
type InMemoryRunner struct {
	m    *Manager
	deps Deps
}

// NewInMemoryRunner membuat runner in-memory (fallback tanpa Redis).
func NewInMemoryRunner(maxConcurrent int, deps Deps) *InMemoryRunner {
	return &InMemoryRunner{m: NewManager(maxConcurrent), deps: deps}
}

// Enqueue menjalankan job lewat Manager in-memory.
func (r *InMemoryRunner) Enqueue(req JobRequest) (*CrawlJob, error) {
	switch req.Kind {
	case "crawl":
		return r.m.Start(req.toCrawlOptions(r.deps))
	case "batch":
		return r.m.StartBatch(req.toBatchOptions(r.deps))
	default:
		return nil, fmt.Errorf("kind tidak dikenal: %q", req.Kind)
	}
}

// Get mengembalikan snapshot job dari Manager.
func (r *InMemoryRunner) Get(id string) *CrawlJob { return r.m.Get(id) }
