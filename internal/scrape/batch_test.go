package scrape

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBatchScrape(t *testing.T) {
	srv := newCrawlSite(t) // dari crawl_test.go: /, /a, /b, /c + robots blokir /rahasia
	m := NewManager(1)
	job, err := m.StartBatch(BatchOptions{
		URLs:              []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/rahasia"},
		Delay:             500 * time.Millisecond,
		OnlyMainContent:   true,
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("StartBatch: %v", err)
	}
	if job.Kind != "batch" {
		t.Errorf("kind = %s, mau batch", job.Kind)
	}
	done := waitJob(t, m, job.ID)
	if done.Status != StatusCompleted {
		t.Fatalf("status = %s, error = %s", done.Status, done.Error)
	}
	if done.Total != 2 {
		t.Fatalf("total = %d, mau 2; errors: %v", done.Total, done.Errors)
	}
	if len(done.Errors) != 1 || !strings.Contains(done.Errors[0].Error, "robots") {
		t.Errorf("mau 1 error robots utk /rahasia, dapat: %v", done.Errors)
	}
}

func TestBatchValidation(t *testing.T) {
	m := NewManager(1)
	if _, err := m.StartBatch(BatchOptions{}); err == nil {
		t.Error("mau error untuk urls kosong")
	}
	if _, err := m.StartBatch(BatchOptions{URLs: []string{"ftp://x"}}); err == nil {
		t.Error("mau error untuk skema ftp")
	}
	urls := make([]string, maxBatchURLs+1)
	for i := range urls {
		urls[i] = "https://example.com/x"
	}
	if _, err := m.StartBatch(BatchOptions{URLs: urls}); err == nil {
		t.Errorf("mau error untuk >%d urls", maxBatchURLs)
	}
}

func TestWebhookDelivery(t *testing.T) {
	t.Setenv("CRAWLER_WEBHOOK_SECRET", "rahasia-uji")

	received := make(chan *http.Request, 1)
	var body []byte
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		received <- r
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hook.Close)

	srv := newCrawlSite(t)
	m := NewManager(1)
	job, err := m.StartBatch(BatchOptions{
		URLs:              []string{srv.URL + "/a"},
		Webhook:           hook.URL,
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("StartBatch: %v", err)
	}
	waitJob(t, m, job.ID)

	select {
	case req := <-received:
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("payload bukan JSON: %v", err)
		}
		if payload["job_id"] != job.ID || payload["status"] != "completed" {
			t.Errorf("payload = %v", payload)
		}
		// Verifikasi HMAC.
		mac := hmac.New(sha256.New, []byte("rahasia-uji"))
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if got := req.Header.Get("X-Signature"); got != want {
			t.Errorf("X-Signature = %q, mau %q", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("webhook tidak diterima dalam 10s")
	}
}

func TestWebhookRetryOn5xx(t *testing.T) {
	attempts := 0
	done := make(chan struct{})
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		close(done)
	}))
	t.Cleanup(hook.Close)

	job := &CrawlJob{ID: "uji", Kind: "batch", Status: StatusCompleted}
	go sendWebhook(hook.URL, job, true)

	select {
	case <-done:
		if attempts != 2 {
			t.Errorf("attempts = %d, mau 2", attempts)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("webhook tidak retry; attempts = %d", attempts)
	}
}

func TestValidateWebhookURL(t *testing.T) {
	if err := ValidateWebhookURL("https://example.com/hook"); err != nil {
		t.Errorf("URL valid ditolak: %v", err)
	}
	for _, bad := range []string{"", "ftp://x/hook", "bukan-url"} {
		if err := ValidateWebhookURL(bad); err == nil {
			t.Errorf("mau error untuk %q", bad)
		}
	}
}

func TestBatchContextCancel(t *testing.T) {
	// Pastikan batchScrape berhenti saat context dibatalkan.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pages, _, err := batchScrape(ctx, BatchOptions{
		URLs:  []string{"https://example.com/a", "https://example.com/b"},
		Delay: time.Second,
	})
	if err == nil || len(pages) != 0 {
		t.Errorf("mau error context canceled tanpa halaman, dapat pages=%d err=%v", len(pages), err)
	}
}
