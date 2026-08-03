package scrape

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
)

// CrawlOptions mengatur satu job crawl BFS pada satu host.
type CrawlOptions struct {
	// URL seed; crawl hanya mengikuti link internal (host sama).
	URL string
	// MaxPages membatasi total halaman yang di-scrape. Default 10, cap 100.
	MaxPages int
	// MaxDepth membatasi kedalaman BFS dari seed (0 = hanya seed). Default 2.
	MaxDepth int
	// Delay antar-request (politeness). Default 1s, minimal 500ms.
	Delay time.Duration
	// OnlyMainContent diteruskan ke setiap scrape halaman. Default true di API.
	OnlyMainContent bool
	// OnPage dipanggil untuk setiap halaman yang sukses di-scrape
	// (mis. persist ke DB). Dipanggil dari goroutine job; error diabaikan
	// oleh crawl — callback bertanggung jawab atas logging-nya sendiri.
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

// CrawlStatus adalah fase hidup sebuah job.
type CrawlStatus string

const (
	StatusPending   CrawlStatus = "pending"
	StatusRunning   CrawlStatus = "running"
	StatusCompleted CrawlStatus = "completed"
	StatusFailed    CrawlStatus = "failed"
)

// PageError mencatat halaman yang gagal di-scrape tanpa menggagalkan job.
type PageError struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

// CrawlJob adalah snapshot state sebuah job (crawl atau batch scrape).
type CrawlJob struct {
	ID          string      `json:"id"`
	Kind        string      `json:"kind"` // "crawl" | "batch"
	Status      CrawlStatus `json:"status"`
	URL         string      `json:"url,omitempty"`
	URLs        []string    `json:"urls,omitempty"`
	MaxPages    int         `json:"max_pages,omitempty"`
	MaxDepth    int         `json:"max_depth,omitempty"`
	Total       int         `json:"total"`
	Pages       []*Result   `json:"pages,omitempty"`
	Errors      []PageError `json:"errors,omitempty"`
	Error       string      `json:"error,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	CompletedAt *time.Time  `json:"completed_at,omitempty"`
}

// jobTTL: job selesai dibuang dari memori setelah durasi ini.
const jobTTL = 30 * time.Minute

// jobTimeout: batas total durasi satu job crawl.
const jobTimeout = 15 * time.Minute

// Manager menyimpan job crawl in-memory dan membatasi job paralel.
type Manager struct {
	mu   sync.Mutex
	jobs map[string]*CrawlJob
	sem  chan struct{}
}

// NewManager membuat Manager dengan maksimal maxConcurrent job berjalan.
func NewManager(maxConcurrent int) *Manager {
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	return &Manager{
		jobs: map[string]*CrawlJob{},
		sem:  make(chan struct{}, maxConcurrent),
	}
}

// ErrInvalidURL menandakan URL seed tidak valid.
var ErrInvalidURL = errors.New("url tidak valid (wajib http/https)")

// Start memvalidasi opsi, mendaftarkan job, dan menjalankan crawl di goroutine.
func (m *Manager) Start(opts CrawlOptions) (*CrawlJob, error) {
	target, err := url.Parse(opts.URL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return nil, fmt.Errorf("%w: %q", ErrInvalidURL, opts.URL)
	}
	if opts.MaxPages <= 0 {
		opts.MaxPages = 10
	}
	if opts.MaxPages > 100 {
		opts.MaxPages = 100
	}
	if opts.MaxDepth < 0 {
		opts.MaxDepth = 0
	}
	if opts.MaxDepth == 0 && opts.MaxPages > 1 {
		opts.MaxDepth = 2
	}
	if opts.Delay < 500*time.Millisecond {
		opts.Delay = time.Second
	}

	job := &CrawlJob{
		ID:        newJobID(),
		Kind:      "crawl",
		Status:    StatusPending,
		URL:       target.String(),
		MaxPages:  opts.MaxPages,
		MaxDepth:  opts.MaxDepth,
		CreatedAt: time.Now().UTC(),
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	go m.run(job.ID, opts.Webhook, opts.AllowPrivateHosts, func(ctx context.Context) ([]*Result, []PageError, error) {
		return crawl(ctx, opts, target)
	})
	return snapshot(job), nil
}

// Get mengembalikan snapshot job, atau nil bila tidak ada/kedaluwarsa.
func (m *Manager) Get(id string) *CrawlJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return nil
	}
	return snapshot(job)
}

// run mengeksekusi sebuah job (crawl/batch) dengan semaphore + timeout,
// menyimpan hasil, lalu mengirim webhook bila dikonfigurasi.
func (m *Manager) run(id, webhook string, allowPrivate bool, exec func(context.Context) ([]*Result, []PageError, error)) {
	m.sem <- struct{}{}
	defer func() { <-m.sem }()

	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	m.update(id, func(j *CrawlJob) { j.Status = StatusRunning })

	pages, pageErrs, err := exec(ctx)

	m.update(id, func(j *CrawlJob) {
		now := time.Now().UTC()
		j.Pages = pages
		j.Errors = pageErrs
		j.Total = len(pages)
		j.CompletedAt = &now
		if err != nil && len(pages) == 0 {
			j.Status = StatusFailed
			j.Error = err.Error()
		} else {
			j.Status = StatusCompleted
			if err != nil {
				j.Error = err.Error()
			}
		}
	})

	if webhook != "" {
		sendWebhook(webhook, m.Get(id), allowPrivate)
	}

	// Bersihkan job lama supaya memori tidak tumbuh tanpa batas.
	time.AfterFunc(jobTTL, func() {
		m.mu.Lock()
		delete(m.jobs, id)
		m.mu.Unlock()
	})
}

func (m *Manager) update(id string, fn func(*CrawlJob)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job, ok := m.jobs[id]; ok {
		fn(job)
	}
}

// crawl menjalankan BFS link internal mulai dari seed.
func crawl(ctx context.Context, opts CrawlOptions, seed *url.URL) ([]*Result, []PageError, error) {
	client := newClient(opts.AllowPrivateHosts)
	robots := loadRobotsData(ctx, client, seed)

	type item struct {
		url   string
		depth int
	}
	queue := []item{{seed.String(), 0}}
	visited := map[string]bool{queue[0].url: true}

	var pages []*Result
	var pageErrs []PageError

	for len(queue) > 0 && len(pages) < opts.MaxPages {
		if err := ctx.Err(); err != nil {
			return pages, pageErrs, err
		}
		cur := queue[0]
		queue = queue[1:]

		if robots != nil {
			if u, err := url.Parse(cur.url); err == nil &&
				!robots.FindGroup(UserAgent).Test(robotsPath(u)) {
				pageErrs = append(pageErrs, PageError{cur.url, ErrBlockedByRobots.Error()})
				continue
			}
		}

		res, err := Scrape(ctx, Options{
			URL:               cur.url,
			OnlyMainContent:   opts.OnlyMainContent,
			CollectLinks:      true,
			SkipRobots:        true,
			RenderJS:          opts.RenderJS,
			Renderer:          opts.Renderer,
			Timeout:           30 * time.Second,
			AllowPrivateHosts: opts.AllowPrivateHosts,
		})
		if err != nil {
			pageErrs = append(pageErrs, PageError{cur.url, err.Error()})
		} else {
			pages = append(pages, res)
			if cur.depth < opts.MaxDepth {
				for _, link := range res.Links {
					if !visited[link] {
						visited[link] = true
						queue = append(queue, item{link, cur.depth + 1})
					}
				}
			}
			// Links tidak perlu ikut di response poll (bisa besar).
			res.Links = nil
			if opts.OnPage != nil {
				opts.OnPage(res)
			}
		}

		if len(queue) > 0 && len(pages) < opts.MaxPages {
			select {
			case <-ctx.Done():
				return pages, pageErrs, ctx.Err()
			case <-time.After(opts.Delay):
			}
		}
	}
	return pages, pageErrs, nil
}

// loadRobotsData memuat robots.txt sekali untuk host seed.
// Gagal ambil dianggap allow-all (nil).
func loadRobotsData(ctx context.Context, client *http.Client, seed *url.URL) *robotstxt.RobotsData {
	robotsURL := seed.Scheme + "://" + seed.Host + "/robots.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	robots, err := robotstxt.FromStatusAndBytes(resp.StatusCode, body)
	if err != nil {
		return nil
	}
	return robots
}

func newJobID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "job-" + hex.EncodeToString([]byte(time.Now().String()))[:16]
	}
	return hex.EncodeToString(b)
}

// snapshot menyalin job supaya pembaca tidak berbagi slice dengan worker.
func snapshot(j *CrawlJob) *CrawlJob {
	cp := *j
	cp.URLs = append([]string(nil), j.URLs...)
	cp.Pages = append([]*Result(nil), j.Pages...)
	cp.Errors = append([]PageError(nil), j.Errors...)
	return &cp
}
