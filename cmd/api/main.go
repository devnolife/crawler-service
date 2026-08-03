// Command api menjalankan HTTP server crawler-service.
//
// Jalankan:
//
//	crawler-api --addr 127.0.0.1:8770
//
// Env:
//
//	CRAWLER_DATABASE_URL             — Postgres DSN
//	CRAWLER_API_KEYS                 — "key:client,key2:client2" (kosong = auth mati)
//	CRAWLER_RATE_LIMIT_PER_MINUTE    — default 120
//	OLLAMA_URL, OLLAMA_MODEL         — untuk title-suggest
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/devnolife/crawler-service/internal/api"
	"github.com/devnolife/crawler-service/internal/db"
)

// defaultAddr membaca CRAWLER_API_HOST/CRAWLER_API_PORT (kompatibel .env lama).
func defaultAddr() string {
	host := os.Getenv("CRAWLER_API_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := os.Getenv("CRAWLER_API_PORT")
	if port == "" {
		port = "8770"
	}
	return host + ":" + port
}

func main() {
	addr := flag.String("addr", defaultAddr(), "alamat listen HTTP")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx)
	if err != nil {
		logger.Error("gagal konek database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.New(pool, logger).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("crawler API listening", "addr", *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
}
