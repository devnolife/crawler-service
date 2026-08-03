package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/devnolife/crawler-service/internal/db"
	"github.com/devnolife/crawler-service/internal/scrape"
)

// ---------------------------------------------------- /api/v1/batch/scrape

type batchRequest struct {
	URLs []string `json:"urls"`
	// OnlyMainContent: buang nav/footer per halaman. Default true.
	OnlyMainContent *bool `json:"only_main_content,omitempty"`
	// DelayMS: jeda antar-request, minimal 500 (default 1000).
	DelayMS int `json:"delay_ms,omitempty"`
	// Persist: simpan tiap halaman ke Postgres (tabel scraped_pages).
	Persist bool `json:"persist,omitempty"`
	// RenderJS: render tiap halaman via browser CDP (Lightpanda).
	RenderJS bool `json:"render_js,omitempty"`
	// Webhook: URL yang di-POST saat job selesai (HMAC bila
	// CRAWLER_WEBHOOK_SECRET di-set).
	Webhook string `json:"webhook,omitempty"`
}

// handleBatchStart: scrape banyak URL sebagai satu job async.
func (s *Server) handleBatchStart(w http.ResponseWriter, r *http.Request) {
	var req batchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "body JSON tidak valid: "+err.Error())
		return
	}
	if len(req.URLs) == 0 {
		writeError(w, http.StatusBadRequest, "field urls wajib diisi")
		return
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
	onlyMain := true
	if req.OnlyMainContent != nil {
		onlyMain = *req.OnlyMainContent
	}
	if _, err := s.validatePersist(r.Context(), req.Persist); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	job, err := s.jobs.Enqueue(scrape.JobRequest{
		Kind:            "batch",
		URLs:            req.URLs,
		DelayMS:         req.DelayMS,
		OnlyMainContent: onlyMain,
		Persist:         req.Persist,
		RenderJS:        req.RenderJS,
		Webhook:         req.Webhook,
	})
	if err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, scrape.ErrInvalidURL) {
			status = http.StatusUnprocessableEntity
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id": job.ID,
		"status": job.Status,
		"total":  len(job.URLs),
	})
}

// validatePersist memastikan Postgres siap bila persist diminta.
func (s *Server) validatePersist(ctx context.Context, persist bool) (bool, error) {
	if !persist {
		return false, nil
	}
	if s.pool == nil {
		return false, errors.New("persist butuh koneksi Postgres")
	}
	if err := db.EnsurePagesSchema(ctx, s.pool); err != nil {
		return false, errors.New("siapkan skema scraped_pages: " + err.Error())
	}
	return true, nil
}

// persistPage meng-upsert satu halaman ke scraped_pages. Dipakai sebagai
// Deps.PersistPage — dipanggil worker hanya bila request.Persist true.
func (s *Server) persistPage(res *scrape.Result) {
	if s.pool == nil {
		return
	}
	host := ""
	if u, err := url.Parse(res.URL); err == nil {
		host = u.Host
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.UpsertPage(ctx, s.pool, host, res); err != nil {
		s.log.Warn("persist halaman gagal", "url", res.URL, "err", err)
	}
}
