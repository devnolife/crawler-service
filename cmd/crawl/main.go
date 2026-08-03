// Command crawl menjalankan crawler EPrints generik.
//
// Contoh:
//
//	# Search UMS "machine learning", 2 halaman, simpan JSON + Postgres.
//	crawler-crawl -base-url https://eprints.ums.ac.id \
//	    -query "machine learning" -max-pages 2 -out output/ums-ml.json
//
//	# Hanya ke Postgres (tanpa file JSON).
//	crawler-crawl -base-url https://digilib.uin-suka.ac.id -query "psikologi" -max-pages 2
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/devnolife/crawler-service/internal/crawler"
	"github.com/devnolife/crawler-service/internal/db"
	"github.com/devnolife/crawler-service/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	baseURL := flag.String("base-url", "https://eprints.ums.ac.id", "base URL repositori EPrints")
	query := flag.String("query", "machine learning", "kata kunci pencarian")
	maxPages := flag.Int("max-pages", 2, "maksimal halaman search")
	delay := flag.Duration("delay", 1500*time.Millisecond, "jeda antar-request")
	out := flag.String("out", "", "path file JSON output (opsional)")
	noDB := flag.Bool("no-db", false, "jangan simpan ke Postgres")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	c, err := crawler.New(ctx, crawler.Config{
		BaseURL:  *baseURL,
		Query:    *query,
		MaxPages: *maxPages,
		Delay:    *delay,
		Logger:   logger,
	})
	if err != nil {
		logger.Error("gagal inisialisasi crawler", "err", err)
		os.Exit(1)
	}

	// Postgres opsional: bila gagal konek, crawl tetap jalan (seperti
	// PostgresPipeline lama yang self-disable).
	var pool *pgxpool.Pool
	if !*noDB {
		if err := db.EnsureSchema(ctx); err != nil {
			logger.Warn("Postgres dinonaktifkan (tidak bisa konek)", "err", err)
		} else if pool, err = db.Connect(ctx); err != nil {
			logger.Warn("Postgres dinonaktifkan (tidak bisa konek)", "err", err)
			pool = nil
		} else {
			defer pool.Close()
			logger.Info("Postgres terhubung", "url", db.URL())
		}
	}

	var papers []model.Paper
	inserted, failed := 0, 0

	err = c.Run(ctx, func(p model.Paper) error {
		if *out != "" {
			papers = append(papers, p)
		}
		if pool != nil {
			if err := db.UpsertPaper(ctx, pool, p); err != nil {
				failed++
				logger.Warn("gagal upsert", "url", p.URL, "err", err)
			} else {
				inserted++
			}
		}
		logger.Info("item", "source_id", p.SourceID, "title", p.Title)
		return nil
	})
	if err != nil {
		logger.Error("crawl gagal", "err", err)
		// Tetap tulis hasil parsial sebelum keluar.
	}

	if *out != "" {
		if err := writeJSON(*out, papers); err != nil {
			logger.Error("gagal tulis output", "path", *out, "err", err)
			os.Exit(1)
		}
		logger.Info("output tersimpan", "path", *out, "items", len(papers))
	}
	if pool != nil {
		logger.Info("ringkasan Postgres", "inserted_updated", inserted, "failed", failed)
	}
	if err != nil {
		os.Exit(1)
	}
}

func writeJSON(path string, papers []model.Paper) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if papers == nil {
		papers = []model.Paper{}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(papers)
}
