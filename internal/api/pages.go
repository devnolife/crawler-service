package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------- /api/v1/pages/search

type scrapedPage struct {
	URL         string    `json:"url"`
	Host        string    `json:"host"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	Language    string    `json:"language,omitempty"`
	Markdown    string    `json:"markdown,omitempty"`
	StatusCode  int       `json:"status_code"`
	ScrapedAt   time.Time `json:"scraped_at"`
}

type pagesSearchResponse struct {
	Query string        `json:"query"`
	Total int64         `json:"total"`
	Items []scrapedPage `json:"items"`
}

// handlePagesSearch mencari halaman hasil crawl yang di-persist
// (FTS di title+markdown). Param: q, host, limit, offset,
// include_markdown=true untuk ikut mengembalikan isi markdown.
func (s *Server) handlePagesSearch(w http.ResponseWriter, r *http.Request) {
	if s.pool == nil {
		writeError(w, http.StatusServiceUnavailable, "butuh koneksi Postgres")
		return
	}
	qp := r.URL.Query()
	q := strings.TrimSpace(qp.Get("q"))

	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	if q != "" {
		where = append(where,
			"to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(markdown,'')) "+
				"@@ plainto_tsquery('simple', "+arg(q)+")")
	}
	if host := qp.Get("host"); host != "" {
		where = append(where, "host = "+arg(host))
	}

	limit := 20
	if v := qp.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	offset := 0
	if v := qp.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	if err := s.pool.QueryRow(r.Context(),
		"SELECT COUNT(*) FROM scraped_pages"+whereSQL, args...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "query gagal: "+err.Error())
		return
	}

	mdCol := "''"
	if qp.Get("include_markdown") == "true" {
		mdCol = "markdown"
	}
	rows, err := s.pool.Query(r.Context(),
		"SELECT url, host, coalesce(title,''), coalesce(description,''), coalesce(language,''), "+
			mdCol+", status_code, scraped_at FROM scraped_pages"+whereSQL+
			" ORDER BY scraped_at DESC LIMIT "+arg(limit)+" OFFSET "+arg(offset), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query gagal: "+err.Error())
		return
	}
	defer rows.Close()

	items := []scrapedPage{}
	for rows.Next() {
		var p scrapedPage
		if err := rows.Scan(&p.URL, &p.Host, &p.Title, &p.Description,
			&p.Language, &p.Markdown, &p.StatusCode, &p.ScrapedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan gagal: "+err.Error())
			return
		}
		items = append(items, p)
	}
	writeJSON(w, http.StatusOK, pagesSearchResponse{Query: q, Total: total, Items: items})
}
