package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/devnolife/crawler-service/internal/scrape"
)

// ----------------------------------------------------------- /api/v1/crawl

type crawlRequest struct {
	URL string `json:"url"`
	// MaxPages: total halaman maksimal (default 10, cap 100).
	MaxPages int `json:"max_pages,omitempty"`
	// MaxDepth: kedalaman BFS dari seed (default 2).
	MaxDepth int `json:"max_depth,omitempty"`
	// DelayMS: jeda antar-request, minimal 500 (default 1000).
	DelayMS int `json:"delay_ms,omitempty"`
	// OnlyMainContent: buang nav/footer per halaman. Default true.
	OnlyMainContent *bool `json:"only_main_content,omitempty"`
	// Persist: simpan tiap halaman ke Postgres (tabel scraped_pages)
	// sehingga bisa dicari lewat GET /api/v1/pages/search.
	Persist bool `json:"persist,omitempty"`
	// RenderJS: render tiap halaman via browser CDP (Lightpanda).
	RenderJS bool `json:"render_js,omitempty"`
	// Webhook: URL yang di-POST saat job selesai (HMAC bila
	// CRAWLER_WEBHOOK_SECRET di-set).
	Webhook string `json:"webhook,omitempty"`
}

// handleCrawlStart: mulai job crawl async, langsung return job_id.
func (s *Server) handleCrawlStart(w http.ResponseWriter, r *http.Request) {
	var req crawlRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "body JSON tidak valid: "+err.Error())
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "field url wajib diisi")
		return
	}
	onlyMain := true
	if req.OnlyMainContent != nil {
		onlyMain = *req.OnlyMainContent
	}
	if req.RenderJS && !s.render.Available() {
		writeError(w, http.StatusUnprocessableEntity, scrape.ErrRenderUnavailable.Error())
		return
	}
	if req.Webhook != "" {
		if err := scrape.ValidateWebhookURL(req.Webhook); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	if _, err := s.validatePersist(r.Context(), req.Persist); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	job, err := s.jobs.Enqueue(scrape.JobRequest{
		Kind:            "crawl",
		URL:             req.URL,
		MaxPages:        req.MaxPages,
		MaxDepth:        req.MaxDepth,
		DelayMS:         req.DelayMS,
		OnlyMainContent: onlyMain,
		Persist:         req.Persist,
		RenderJS:        req.RenderJS,
		Webhook:         req.Webhook,
	})
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, scrape.ErrInvalidURL) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id": job.ID,
		"status": job.Status,
		"url":    job.URL,
	})
}

// handleCrawlStatus: poll status + hasil job crawl/batch.
func (s *Server) handleCrawlStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job := s.jobs.Get(id)
	if job == nil {
		writeError(w, http.StatusNotFound, "job tidak ditemukan (mungkin sudah kedaluwarsa)")
		return
	}
	writeJSON(w, http.StatusOK, job)
}
