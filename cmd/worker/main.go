// Command worker menjalankan worker queue crawl/batch tanpa HTTP API.
//
// Untuk fleet: jalankan N instance di mesin berbeda, semuanya menunjuk
// Redis yang sama — asynq membagi job otomatis antar worker (termasuk
// worker embedded di crawler-api).
//
//	crawler-worker
//
// Env:
//
//	CRAWLER_REDIS_ADDR        — wajib (mis. 127.0.0.1:6379)
//	CRAWLER_CRAWL_CONCURRENCY — job paralel per worker (default 2)
//	CRAWLER_DATABASE_URL      — opsional; dipakai bila job minta persist
//	CRAWLER_CDP_URL           — opsional; endpoint CDP utk render_js (boleh banyak)
//	CRAWLER_PROXY_URLS        — opsional; rotasi proxy
//	CRAWLER_WEBHOOK_SECRET    — opsional; HMAC signature webhook
package main

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/devnolife/crawler-service/internal/db"
	"github.com/devnolife/crawler-service/internal/scrape"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	redisAddr := os.Getenv("CRAWLER_REDIS_ADDR")
	if redisAddr == "" {
		logger.Error("CRAWLER_REDIS_ADDR wajib di-set untuk worker")
		os.Exit(1)
	}

	concurrency := 2
	if v := os.Getenv("CRAWLER_CRAWL_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			concurrency = n
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Postgres opsional — hanya untuk job dengan persist:true.
	var pool *pgxpool.Pool
	if p, err := db.Connect(ctx); err == nil {
		if err := p.Ping(ctx); err == nil {
			pool = p
			defer pool.Close()
			if err := db.EnsurePagesSchema(ctx, pool); err != nil {
				logger.Warn("skema scraped_pages gagal disiapkan", "err", err)
			}
		} else {
			p.Close()
			logger.Warn("Postgres tidak terjangkau; persist dinonaktifkan", "err", err)
		}
	} else {
		logger.Warn("Postgres tidak terkonfigurasi; persist dinonaktifkan", "err", err)
	}

	deps := scrape.Deps{
		Renderer: scrape.NewRendererFromEnv(),
		PersistPage: func(res *scrape.Result) {
			if pool == nil {
				return
			}
			host := ""
			if u, err := url.Parse(res.URL); err == nil {
				host = u.Host
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := db.UpsertPage(ctx, pool, host, res); err != nil {
				logger.Warn("persist halaman gagal", "url", res.URL, "err", err)
			}
		},
	}

	runner, err := scrape.NewRedisRunner(redisAddr, concurrency, deps, logger)
	if err != nil {
		logger.Error("gagal start worker", "err", err)
		os.Exit(1)
	}
	defer runner.Close()

	logger.Info("crawler-worker jalan", "redis", redisAddr, "concurrency", concurrency)
	<-ctx.Done()
	logger.Info("shutdown")
}
