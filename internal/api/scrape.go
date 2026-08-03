package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/devnolife/crawler-service/internal/scrape"
)

// ---------------------------------------------------------- /api/v1/scrape

type scrapeRequest struct {
	URL string `json:"url"`
	// OnlyMainContent: buang nav/footer via readability. Default true.
	OnlyMainContent *bool `json:"only_main_content,omitempty"`
	// TimeoutMS membatasi fetch (1000–60000 ms). Default 30000.
	TimeoutMS int `json:"timeout_ms,omitempty"`
	// RenderJS: render via browser CDP (Lightpanda) untuk situs SPA.
	// Butuh env CRAWLER_CDP_URL di server.
	RenderJS bool `json:"render_js,omitempty"`
}

// handleScrape: scraping on-demand — fetch URL saat request, ekstrak konten
// utama, return markdown. Tidak membaca/menulis Postgres.
func (s *Server) handleScrape(w http.ResponseWriter, r *http.Request) {
	var req scrapeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "body JSON tidak valid: "+err.Error())
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "field url wajib diisi")
		return
	}

	timeout := 30 * time.Second
	if req.TimeoutMS > 0 {
		if req.TimeoutMS < 1000 || req.TimeoutMS > 60000 {
			writeError(w, http.StatusBadRequest, "timeout_ms harus 1000–60000")
			return
		}
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	onlyMain := true
	if req.OnlyMainContent != nil {
		onlyMain = *req.OnlyMainContent
	}
	if req.RenderJS && !s.render.Available() {
		writeError(w, http.StatusUnprocessableEntity, scrape.ErrRenderUnavailable.Error())
		return
	}

	res, err := scrape.Scrape(r.Context(), scrape.Options{
		URL:             req.URL,
		OnlyMainContent: onlyMain,
		Timeout:         timeout,
		RenderJS:        req.RenderJS,
		Renderer:        s.render,
	})
	if err != nil {
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, scrape.ErrBlockedByRobots):
			status = http.StatusForbidden
		case errors.Is(err, r.Context().Err()):
			status = http.StatusGatewayTimeout
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
