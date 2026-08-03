package scrape

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

const testRedisAddr = "127.0.0.1:6379"

// newTestRedisRunner membuat RedisRunner terhadap Redis lokal.
// Skip bila Redis tidak terjangkau.
func newTestRedisRunner(t *testing.T, deps Deps) *RedisRunner {
	t.Helper()
	conn, err := net.DialTimeout("tcp", testRedisAddr, time.Second)
	if err != nil {
		t.Skipf("Redis %s tidak terjangkau: %v", testRedisAddr, err)
	}
	conn.Close()

	r, err := NewRedisRunner(testRedisAddr, 1, deps, nil)
	if err != nil {
		t.Fatalf("NewRedisRunner: %v", err)
	}
	t.Cleanup(r.Close)
	return r
}

func waitRedisJob(t *testing.T, r *RedisRunner, id string) *CrawlJob {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		job := r.Get(id)
		if job != nil && (job.Status == StatusCompleted || job.Status == StatusFailed) {
			return job
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("job Redis tidak selesai dalam 30s")
	return nil
}

func TestRedisRunnerBatch(t *testing.T) {
	srv := newCrawlSite(t)
	r := newTestRedisRunner(t, Deps{})

	job, err := r.Enqueue(JobRequest{
		Kind:              "batch",
		URLs:              []string{srv.URL + "/a", srv.URL + "/b"},
		DelayMS:           500,
		OnlyMainContent:   true,
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if job.Status != StatusPending || job.Kind != "batch" {
		t.Errorf("job awal = %+v", job)
	}

	done := waitRedisJob(t, r, job.ID)
	if done.Status != StatusCompleted {
		t.Fatalf("status = %s, error = %s", done.Status, done.Error)
	}
	if done.Total != 2 {
		t.Fatalf("total = %d, mau 2; errors: %v", done.Total, done.Errors)
	}
	if done.Pages[0].Markdown == "" {
		t.Error("markdown kosong di hasil dari Redis")
	}
}

func TestRedisRunnerCrawl(t *testing.T) {
	srv := newCrawlSite(t)
	r := newTestRedisRunner(t, Deps{})

	job, err := r.Enqueue(JobRequest{
		Kind:              "crawl",
		URL:               srv.URL + "/",
		MaxPages:          3,
		MaxDepth:          2,
		DelayMS:           500,
		OnlyMainContent:   true,
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	done := waitRedisJob(t, r, job.ID)
	if done.Status != StatusCompleted {
		t.Fatalf("status = %s, error = %s", done.Status, done.Error)
	}
	if done.Total != 3 {
		t.Fatalf("total = %d, mau 3 (max_pages)", done.Total)
	}
}

func TestRedisRunnerValidation(t *testing.T) {
	r := newTestRedisRunner(t, Deps{})
	if _, err := r.Enqueue(JobRequest{Kind: "crawl", URL: "ftp://x"}); err == nil {
		t.Error("mau error untuk URL ftp")
	}
	if _, err := r.Enqueue(JobRequest{Kind: "batch"}); err == nil {
		t.Error("mau error untuk urls kosong")
	}
	if _, err := r.Enqueue(JobRequest{Kind: "aneh"}); err == nil {
		t.Error("mau error untuk kind tidak dikenal")
	}
	if job := r.Get("tidak-ada"); job != nil {
		t.Errorf("mau nil untuk job tak dikenal, dapat %+v", job)
	}
}

func TestRedisRunnerStateSurvives(t *testing.T) {
	// State job dibaca langsung dari Redis — bisa diambil instance lain.
	srv := newCrawlSite(t)
	r := newTestRedisRunner(t, Deps{})

	job, err := r.Enqueue(JobRequest{
		Kind:              "batch",
		URLs:              []string{srv.URL + "/a"},
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitRedisJob(t, r, job.ID)

	// Klien Redis terpisah (simulasi instance API lain) membaca state.
	rdb := redis.NewClient(&redis.Options{Addr: testRedisAddr})
	defer rdb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := rdb.Get(ctx, jobKey(job.ID)).Bytes()
	if err != nil || len(raw) == 0 {
		t.Fatalf("state job tidak ada di Redis: %v", err)
	}
}

func TestInMemoryRunner(t *testing.T) {
	srv := newCrawlSite(t)
	r := NewInMemoryRunner(1, Deps{})
	job, err := r.Enqueue(JobRequest{
		Kind:              "batch",
		URLs:              []string{srv.URL + "/a"},
		OnlyMainContent:   true,
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if j := r.Get(job.ID); j != nil && j.Status == StatusCompleted {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("job in-memory tidak selesai")
}
