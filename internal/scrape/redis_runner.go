package scrape

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// taskTypeScrapeJob adalah tipe task asynq untuk job crawl/batch.
const taskTypeScrapeJob = "scrape:job"

// redisJobTTL: state job disimpan di Redis selama durasi ini.
const redisJobTTL = 30 * time.Minute

// redisPayload adalah isi task asynq.
type redisPayload struct {
	JobID   string     `json:"job_id"`
	Request JobRequest `json:"request"`
}

// RedisRunner menjalankan job lewat queue asynq + Redis: job tahan restart
// proses, retry otomatis, dan state bisa dibaca lintas instance.
type RedisRunner struct {
	client *asynq.Client
	server *asynq.Server
	rdb    *redis.Client
	deps   Deps
	log    *slog.Logger
}

// NewRedisRunner menghubungkan ke Redis addr (mis. "127.0.0.1:6379"),
// menjalankan worker embedded dengan concurrency tertentu, dan
// mengembalikan runner durable.
func NewRedisRunner(addr string, concurrency int, deps Deps, logger *slog.Logger) (*RedisRunner, error) {
	if concurrency <= 0 {
		concurrency = 2
	}
	if logger == nil {
		logger = slog.Default()
	}
	opt := asynq.RedisClientOpt{Addr: addr}

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("redis %s tidak terjangkau: %w", addr, err)
	}

	r := &RedisRunner{
		client: asynq.NewClient(opt),
		rdb:    rdb,
		deps:   deps,
		log:    logger,
	}

	r.server = asynq.NewServer(opt, asynq.Config{
		Concurrency: concurrency,
		Queues:      map[string]int{"scrape": 1},
		Logger:      asynqLogger{logger},
	})
	mux := asynq.NewServeMux()
	mux.HandleFunc(taskTypeScrapeJob, r.handleTask)
	if err := r.server.Start(mux); err != nil {
		r.client.Close()
		rdb.Close()
		return nil, fmt.Errorf("start worker asynq: %w", err)
	}
	return r, nil
}

// Enqueue memvalidasi request, menyimpan state pending, dan mendorong task.
func (r *RedisRunner) Enqueue(req JobRequest) (*CrawlJob, error) {
	job := &CrawlJob{
		ID:        newJobID(),
		Kind:      req.Kind,
		Status:    StatusPending,
		CreatedAt: time.Now().UTC(),
	}
	switch req.Kind {
	case "crawl":
		target, err := url.Parse(req.URL)
		if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
			return nil, fmt.Errorf("%w: %q", ErrInvalidURL, req.URL)
		}
		job.URL = target.String()
		job.MaxPages = req.MaxPages
		job.MaxDepth = req.MaxDepth
	case "batch":
		if len(req.URLs) == 0 {
			return nil, fmt.Errorf("%w: urls kosong", ErrInvalidURL)
		}
		if len(req.URLs) > maxBatchURLs {
			return nil, fmt.Errorf("maksimal %d url per batch, dapat %d", maxBatchURLs, len(req.URLs))
		}
		for _, raw := range req.URLs {
			u, err := url.Parse(raw)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, fmt.Errorf("%w: %q", ErrInvalidURL, raw)
			}
		}
		job.URLs = req.URLs
	default:
		return nil, fmt.Errorf("kind tidak dikenal: %q", req.Kind)
	}

	if err := r.saveJob(job); err != nil {
		return nil, err
	}

	payload, err := json.Marshal(redisPayload{JobID: job.ID, Request: req})
	if err != nil {
		return nil, err
	}
	_, err = r.client.Enqueue(
		asynq.NewTask(taskTypeScrapeJob, payload),
		asynq.Queue("scrape"),
		asynq.MaxRetry(2),
		asynq.Timeout(jobTimeout),
		asynq.Retention(redisJobTTL),
	)
	if err != nil {
		return nil, fmt.Errorf("enqueue: %w", err)
	}
	return job, nil
}

// Get membaca snapshot job dari Redis; nil bila tidak ada/kedaluwarsa.
func (r *RedisRunner) Get(id string) *CrawlJob {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := r.rdb.Get(ctx, jobKey(id)).Bytes()
	if err != nil {
		return nil
	}
	var job CrawlJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil
	}
	return &job
}

// Close menghentikan worker dan koneksi.
func (r *RedisRunner) Close() {
	r.server.Shutdown()
	r.client.Close()
	r.rdb.Close()
}

// handleTask mengeksekusi satu job dari queue.
func (r *RedisRunner) handleTask(ctx context.Context, t *asynq.Task) error {
	var p redisPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("payload rusak: %w: %w", err, asynq.SkipRetry)
	}
	job := r.Get(p.JobID)
	if job == nil {
		// State kedaluwarsa — tidak ada gunanya diproses ulang.
		return nil
	}
	job.Status = StatusRunning
	if err := r.saveJob(job); err != nil {
		return err
	}

	var (
		pages    []*Result
		pageErrs []PageError
		err      error
	)
	switch p.Request.Kind {
	case "crawl":
		target, perr := url.Parse(p.Request.URL)
		if perr != nil {
			return fmt.Errorf("url rusak: %w: %w", perr, asynq.SkipRetry)
		}
		pages, pageErrs, err = crawl(ctx, p.Request.toCrawlOptions(r.deps), target)
	case "batch":
		pages, pageErrs, err = batchScrape(ctx, p.Request.toBatchOptions(r.deps))
	default:
		return fmt.Errorf("kind %q: %w", p.Request.Kind, asynq.SkipRetry)
	}

	now := time.Now().UTC()
	job.Pages = pages
	job.Errors = pageErrs
	job.Total = len(pages)
	job.CompletedAt = &now
	if err != nil && len(pages) == 0 {
		job.Status = StatusFailed
		job.Error = err.Error()
	} else {
		job.Status = StatusCompleted
		if err != nil {
			job.Error = err.Error()
		}
	}
	if serr := r.saveJob(job); serr != nil {
		return serr
	}

	if p.Request.Webhook != "" {
		sendWebhook(p.Request.Webhook, job, p.Request.AllowPrivateHosts)
	}
	return nil
}

func (r *RedisRunner) saveJob(job *CrawlJob) error {
	raw, err := json.Marshal(job)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.rdb.Set(ctx, jobKey(job.ID), raw, redisJobTTL).Err()
}

func jobKey(id string) string { return "crawler:job:" + id }

// asynqLogger menjembatani logger asynq ke slog.
type asynqLogger struct{ l *slog.Logger }

func (a asynqLogger) Debug(args ...any) { a.l.Debug(fmt.Sprint(args...)) }
func (a asynqLogger) Info(args ...any)  { a.l.Info(fmt.Sprint(args...)) }
func (a asynqLogger) Warn(args ...any)  { a.l.Warn(fmt.Sprint(args...)) }
func (a asynqLogger) Error(args ...any) { a.l.Error(fmt.Sprint(args...)) }
func (a asynqLogger) Fatal(args ...any) { a.l.Error("FATAL: " + fmt.Sprint(args...)) }
