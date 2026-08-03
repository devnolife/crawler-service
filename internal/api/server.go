// Package api mengekspos data papers hasil crawl lewat HTTP.
//
// Shared crawling service — dipakai lintas project (studio-revisi,
// wizard-research, dll) lewat API key per client.
//
// Auth (opsional, aktif bila env di-set):
//
//	CRAWLER_API_KEYS="<key>:<client>,<key2>:<client2>"
//	Request wajib kirim header `X-API-Key`. /health tetap publik.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/devnolife/crawler-service/internal/ollama"
	"github.com/devnolife/crawler-service/internal/scrape"
)

// Server memegang dependensi bersama seluruh handler.
type Server struct {
	pool   *pgxpool.Pool
	llm    *ollama.Client
	log    *slog.Logger
	keys   map[string]string // api key -> nama client
	limit  int               // request per menit per client
	jobs   scrape.Runner     // job crawl/batch async (in-memory atau Redis)
	render *scrape.Renderer  // browser CDP (Lightpanda) utk render_js
	mu     sync.Mutex
	window map[string][]time.Time
}

// New membangun Server dari environment.
func New(pool *pgxpool.Pool, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	limit := 120
	if v := os.Getenv("CRAWLER_RATE_LIMIT_PER_MINUTE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	s := &Server{
		pool:   pool,
		llm:    ollama.NewFromEnv(),
		log:    logger,
		keys:   parseAPIKeys(os.Getenv("CRAWLER_API_KEYS")),
		limit:  limit,
		render: scrape.NewRendererFromEnv(),
		window: map[string][]time.Time{},
	}
	s.jobs = newRunner(s, logger)
	return s
}

// newRunner memilih Runner: Redis (durable) bila CRAWLER_REDIS_ADDR di-set
// dan terjangkau, selain itu in-memory.
func newRunner(s *Server, logger *slog.Logger) scrape.Runner {
	deps := scrape.Deps{
		Renderer:    s.render,
		PersistPage: s.persistPage,
	}
	if addr := strings.TrimSpace(os.Getenv("CRAWLER_REDIS_ADDR")); addr != "" {
		r, err := scrape.NewRedisRunner(addr, crawlConcurrency(), deps, logger)
		if err != nil {
			logger.Warn("Redis runner gagal, fallback in-memory", "addr", addr, "err", err)
		} else {
			logger.Info("job runner: Redis/asynq", "addr", addr)
			return r
		}
	}
	return scrape.NewInMemoryRunner(crawlConcurrency(), deps)
}

// crawlConcurrency membaca CRAWLER_CRAWL_CONCURRENCY (default 2).
func crawlConcurrency() int {
	if v := os.Getenv("CRAWLER_CRAWL_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 2
}

// parseAPIKeys mem-parse "key1:studio-revisi,key2:wizard-research".
// Label client opsional; tanpa label memakai "default". Kosong = auth mati.
func parseAPIKeys(raw string) map[string]string {
	keys := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, client, _ := strings.Cut(part, ":")
		client = strings.TrimSpace(client)
		if client == "" {
			client = "default"
		}
		keys[strings.TrimSpace(key)] = client
	}
	return keys
}

// Handler merakit router + middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/datasets/search", s.handleSearch)
	mux.HandleFunc("GET /api/v1/datasets/trend", s.handleTrend)
	mux.HandleFunc("POST /api/v1/citations/suggest", s.handleCitations)
	mux.HandleFunc("POST /api/v1/similarity/check", s.handleSimilarity)
	mux.HandleFunc("POST /api/v1/research/title-suggest", s.handleTitleSuggest)
	mux.HandleFunc("POST /api/v1/scrape", s.handleScrape)
	mux.HandleFunc("POST /api/v1/crawl", s.handleCrawlStart)
	mux.HandleFunc("GET /api/v1/crawl/{id}", s.handleCrawlStatus)
	mux.HandleFunc("POST /api/v1/extract", s.handleExtract)
	mux.HandleFunc("POST /api/v1/map", s.handleMap)
	mux.HandleFunc("GET /api/v1/pages/search", s.handlePagesSearch)
	mux.HandleFunc("POST /api/v1/batch/scrape", s.handleBatchStart)
	mux.HandleFunc("GET /api/v1/batch/scrape/{id}", s.handleCrawlStatus)
	return s.middleware(mux)
}

var publicPaths = map[string]bool{"/health": true}

// middleware: CORS + auth API key + sliding-window rate limit per client.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if publicPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		client := "anonymous"
		if len(s.keys) > 0 {
			c, ok := s.keys[r.Header.Get("X-API-Key")]
			if !ok {
				writeError(w, http.StatusUnauthorized, "invalid or missing X-API-Key")
				return
			}
			client = c
		}

		if !s.allow(client) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests,
				"rate limit exceeded ("+strconv.Itoa(s.limit)+"/min)")
			return
		}

		w.Header().Set("X-Client", client)
		next.ServeHTTP(w, r.WithContext(withClient(r.Context(), client)))
	})
}

// allow menerapkan sliding-window 60 detik per client (in-memory, 1 proses).
func (s *Server) allow(client string) bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	win := s.window[client]
	i := 0
	for ; i < len(win); i++ {
		if now.Sub(win[i]) <= time.Minute {
			break
		}
	}
	win = win[i:]
	if len(win) >= s.limit {
		s.window[client] = win
		return false
	}
	s.window[client] = append(win, now)
	return true
}

type ctxKey int

const clientKey ctxKey = 0

func withClient(ctx context.Context, client string) context.Context {
	return context.WithValue(ctx, clientKey, client)
}

// ---------------------------------------------------------------- helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}
