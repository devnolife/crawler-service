package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/devnolife/crawler-service/internal/model"
)

const paperColumns = `id, source, source_id, title, authors, journal, year, url,
       abstract, keywords, is_open_access, has_dataset, dataset_urls,
       to_char(scraped_at, 'YYYY-MM-DD"T"HH24:MI:SSOF') AS scraped_at`

func scanPaper(row pgx.Row) (model.Paper, error) {
	var p model.Paper
	err := row.Scan(&p.ID, &p.Source, &p.SourceID, &p.Title, &p.Authors,
		&p.Journal, &p.Year, &p.URL, &p.Abstract, &p.Keywords,
		&p.IsOpenAccess, &p.HasDataset, &p.DatasetURLs, &p.ScrapedAt)
	if p.Authors == nil {
		p.Authors = []string{}
	}
	if p.Keywords == nil {
		p.Keywords = []string{}
	}
	if p.DatasetURLs == nil {
		p.DatasetURLs = []string{}
	}
	return p, err
}

// ---------------------------------------------------------------- /health

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var count int64
	if err := s.pool.QueryRow(r.Context(), "SELECT COUNT(*) FROM papers").Scan(&count); err != nil {
		writeError(w, http.StatusServiceUnavailable, "db unavailable: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "papers": count})
}

// ------------------------------------------------ /api/v1/datasets/search

type searchResponse struct {
	Query string        `json:"query"`
	Total int64         `json:"total"`
	Items []model.Paper `json:"items"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	qp := r.URL.Query()
	q := qp.Get("q")

	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	if strings.TrimSpace(q) != "" {
		where = append(where,
			"to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,'')) "+
				"@@ plainto_tsquery('simple', "+arg(q)+")")
	}
	if src := qp.Get("source"); src != "" {
		where = append(where, "source = "+arg(src))
	}
	if v := qp.Get("year_min"); v != "" {
		n, err := parseIntRange(v, 1900, 2100)
		if err != nil {
			writeError(w, http.StatusBadRequest, "year_min invalid")
			return
		}
		where = append(where, "year >= "+arg(n))
	}
	if v := qp.Get("year_max"); v != "" {
		n, err := parseIntRange(v, 1900, 2100)
		if err != nil {
			writeError(w, http.StatusBadRequest, "year_max invalid")
			return
		}
		where = append(where, "year <= "+arg(n))
	}
	if v := qp.Get("has_dataset"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "has_dataset invalid")
			return
		}
		where = append(where, "has_dataset = "+arg(b))
	}

	limit := clampQueryInt(qp.Get("limit"), 20, 1, 100)
	offset := clampQueryInt(qp.Get("offset"), 0, 0, 1<<30)

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	ctx := r.Context()
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM papers "+whereSQL, args...).Scan(&total); err != nil {
		writeError(w, http.StatusServiceUnavailable, "db error: "+err.Error())
		return
	}

	listSQL := "SELECT " + paperColumns + " FROM papers " + whereSQL +
		" ORDER BY year DESC NULLS LAST, scraped_at DESC" +
		" LIMIT " + arg(limit) + " OFFSET " + arg(offset)

	items, err := s.queryPapers(ctx, listSQL, args...)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "db error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, searchResponse{Query: q, Total: total, Items: items})
}

func (s *Server) queryPapers(ctx context.Context, sql string, args ...any) ([]model.Paper, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.Paper{}
	for rows.Next() {
		p, err := scanPaper(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

// ------------------------------------------------- /api/v1/datasets/trend

type trendPoint struct {
	Year  int `json:"year"`
	Count int `json:"count"`
}

type trendResponse struct {
	Query  string       `json:"query"`
	Total  int          `json:"total"`
	Series []trendPoint `json:"series"`
}

// handleTrend meng-agregasi jumlah paper per tahun untuk topik tertentu.
func (s *Server) handleTrend(w http.ResponseWriter, r *http.Request) {
	qp := r.URL.Query()
	q := qp.Get("q")
	yearMin := clampQueryInt(qp.Get("year_min"), 2015, 1900, 2100)
	yearMax := clampQueryInt(qp.Get("year_max"), 2030, 1900, 2100)

	where := []string{"year IS NOT NULL", "year BETWEEN $1 AND $2"}
	args := []any{yearMin, yearMax}
	if strings.TrimSpace(q) != "" {
		args = append(args, q)
		where = append(where,
			"to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,'')) "+
				"@@ plainto_tsquery('simple', $3)")
	}
	sql := `SELECT year, COUNT(*)::int FROM papers WHERE ` +
		strings.Join(where, " AND ") + ` GROUP BY year ORDER BY year`

	rows, err := s.pool.Query(r.Context(), sql, args...)
	if err != nil {
		s.log.Warn("trend query failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "db error: "+err.Error())
		return
	}
	defer rows.Close()

	series := []trendPoint{}
	total := 0
	for rows.Next() {
		var p trendPoint
		if err := rows.Scan(&p.Year, &p.Count); err != nil {
			writeError(w, http.StatusServiceUnavailable, "db error: "+err.Error())
			return
		}
		series = append(series, p)
		total += p.Count
	}
	writeJSON(w, http.StatusOK, trendResponse{Query: q, Total: total, Series: series})
}

// ---------------------------------------------------------------- helpers

func parseIntRange(v string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, strconv.ErrRange
	}
	return n, nil
}

func clampQueryInt(v string, def, lo, hi int) int {
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
