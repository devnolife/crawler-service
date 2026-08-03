package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/devnolife/crawler-service/internal/scrape"
)

// ------------------------------------------------------------- /api/v1/map

type mapRequest struct {
	URL string `json:"url"`
	// Limit maksimal URL (default 100, cap 5000).
	Limit int `json:"limit,omitempty"`
	// Search memfilter URL yang mengandung substring ini.
	Search string `json:"search,omitempty"`
}

// handleMap: link discovery cepat — sitemap.xml (plus robots.txt Sitemap:)
// dengan fallback BFS ringan. Tidak mengonversi konten.
func (s *Server) handleMap(w http.ResponseWriter, r *http.Request) {
	var req mapRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "body JSON tidak valid: "+err.Error())
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "field url wajib diisi")
		return
	}

	res, err := scrape.Map(r.Context(), scrape.MapOptions{
		URL:    req.URL,
		Limit:  req.Limit,
		Search: req.Search,
	})
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, scrape.ErrInvalidURL) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
