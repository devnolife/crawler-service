package scrape

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"
)

// webhookPayload adalah notifikasi ringkas saat job selesai.
// Konsumen mengambil hasil lengkap lewat GET /api/v1/crawl/{id}.
type webhookPayload struct {
	JobID       string      `json:"job_id"`
	Kind        string      `json:"kind"`
	Status      CrawlStatus `json:"status"`
	URL         string      `json:"url,omitempty"`
	Total       int         `json:"total"`
	ErrorCount  int         `json:"error_count"`
	Error       string      `json:"error,omitempty"`
	CompletedAt *time.Time  `json:"completed_at,omitempty"`
}

// ValidateWebhookURL memeriksa webhook layak dipakai (http/https, ada host).
// Resolusi ke IP privat dicegah saat pengiriman oleh guard dial newClient.
func ValidateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: webhook %q", ErrInvalidURL, raw)
	}
	return nil
}

// sendWebhook mem-POST payload ringkas ke webhook dengan retry 3x
// (backoff 1s/4s/9s). Bila env CRAWLER_WEBHOOK_SECRET di-set, request
// ditandatangani HMAC-SHA256 di header X-Signature ("sha256=<hex>").
func sendWebhook(webhook string, job *CrawlJob, allowPrivate bool) {
	if job == nil {
		return
	}
	payload := webhookPayload{
		JobID:       job.ID,
		Kind:        job.Kind,
		Status:      job.Status,
		URL:         job.URL,
		Total:       job.Total,
		ErrorCount:  len(job.Errors),
		Error:       job.Error,
		CompletedAt: job.CompletedAt,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	// Guard anti-SSRF yang sama dengan scrape: webhook milik user,
	// jangan biarkan menembak layanan internal.
	client := newClient(allowPrivate)

	var signature string
	if secret := os.Getenv("CRAWLER_WEBHOOK_SECRET"); secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		signature = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}

	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
		if err != nil {
			cancel()
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", UserAgent)
		if signature != "" {
			req.Header.Set("X-Signature", signature)
		}

		resp, err := client.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		slog.Warn("webhook gagal", "job", job.ID, "attempt", attempt, "err", err)
	}
}
