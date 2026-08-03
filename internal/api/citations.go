package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/devnolife/crawler-service/internal/model"
)

// ----------------------------------------------- /api/v1/citations/suggest

type citationSuggestion struct {
	Paper  model.Paper `json:"paper"`
	BibTeX string      `json:"bibtex"`
	APA    string      `json:"apa"`
}

type citationResponse struct {
	ParagraphSnippet string               `json:"paragraph_snippet"`
	KeywordsUsed     []string             `json:"keywords_used"`
	Suggestions      []citationSuggestion `json:"suggestions"`
}

// handleCitations: saran sitasi dari paragraf user — top-K paper relevan
// + format BibTeX/APA.
func (s *Server) handleCitations(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Paragraph string `json:"paragraph"`
		Limit     int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "body JSON tidak valid")
		return
	}
	paragraph := strings.TrimSpace(payload.Paragraph)
	if len(paragraph) < 20 {
		writeError(w, http.StatusBadRequest, "paragraph minimal 20 karakter")
		return
	}
	limit := payload.Limit
	if limit < 1 || limit > 20 {
		limit = 5
	}

	keywords := extractKeywords(paragraph, 6)
	resp := citationResponse{
		ParagraphSnippet: truncate(paragraph, 160),
		KeywordsUsed:     keywords,
		Suggestions:      []citationSuggestion{},
	}
	// OR-join keyword agar match lebih lenient (≥1 kata cocok cukup).
	safe := sanitizeKeywords(keywords)
	if len(safe) == 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	tsQuery := strings.Join(safe, " | ")

	sql := `SELECT ` + paperColumns + `
          FROM papers
         WHERE to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,''))
               @@ to_tsquery('simple', $1)
         ORDER BY ts_rank(to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,'')),
                          to_tsquery('simple', $1)) DESC,
                  year DESC NULLS LAST
         LIMIT $2`
	papers, err := s.queryPapers(r.Context(), sql, tsQuery, limit)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "db error: "+err.Error())
		return
	}
	for _, p := range papers {
		resp.Suggestions = append(resp.Suggestions, citationSuggestion{
			Paper:  p,
			BibTeX: formatBibTeX(p),
			APA:    formatAPA(p),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// ------------------------------------------------ /api/v1/similarity/check

type similarityHit struct {
	Paper        model.Paper `json:"paper"`
	Score        float64     `json:"score"`
	MatchedTerms []string    `json:"matched_terms"`
}

type similarityResponse struct {
	InputExcerpt string          `json:"input_excerpt"`
	WordCount    int             `json:"word_count"`
	RiskLevel    string          `json:"risk_level"` // low | medium | high
	TopScore     float64         `json:"top_score"`
	Hits         []similarityHit `json:"hits"`
}

// handleSimilarity: cek mirip tidaknya teks user vs koleksi paper di DB.
//
// Pendekatan: ekstrak kata signifikan → ts_rank Postgres FTS → top-N paper
// paling mirip. Score 0..1. Risk: low (<0.05), medium (<0.15), high (>=0.15).
func (s *Server) handleSimilarity(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Text  string `json:"text"`
		Limit int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "body JSON tidak valid")
		return
	}
	text := strings.TrimSpace(payload.Text)
	if len(text) < 50 {
		writeError(w, http.StatusBadRequest, "text minimal 50 karakter")
		return
	}
	limit := payload.Limit
	if limit < 1 || limit > 20 {
		limit = 5
	}

	keywords := extractKeywords(text, 12)
	resp := similarityResponse{
		InputExcerpt: truncate(text, 200),
		WordCount:    len(strings.Fields(text)),
		RiskLevel:    "low",
		Hits:         []similarityHit{},
	}
	safe := sanitizeKeywords(keywords)
	if len(safe) == 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	tsQuery := strings.Join(safe, " | ")

	sql := `SELECT ` + paperColumns + `,
               ts_rank(to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,'')),
                       to_tsquery('simple', $1)) AS rank
          FROM papers
         WHERE to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,''))
               @@ to_tsquery('simple', $1)
         ORDER BY rank DESC
         LIMIT $2`

	rows, err := s.pool.Query(r.Context(), sql, tsQuery, limit)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "db error: "+err.Error())
		return
	}
	defer rows.Close()

	topScore := 0.0
	for rows.Next() {
		var p model.Paper
		var rank *float32
		err := rows.Scan(&p.ID, &p.Source, &p.SourceID, &p.Title, &p.Authors,
			&p.Journal, &p.Year, &p.URL, &p.Abstract, &p.Keywords,
			&p.IsOpenAccess, &p.HasDataset, &p.DatasetURLs, &p.ScrapedAt, &rank)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "db error: "+err.Error())
			return
		}
		if p.Authors == nil {
			p.Authors = []string{}
		}
		if p.Keywords == nil {
			p.Keywords = []string{}
		}
		if p.DatasetURLs == nil {
			p.DatasetURLs = []string{}
		}

		score := 0.0
		if rank != nil {
			score = min(1.0, max(0.0, float64(*rank)))
		}
		if score > topScore {
			topScore = score
		}

		// Cari keyword user mana yang muncul di title/abstract.
		haystack := strings.ToLower(p.Title)
		if p.Abstract != nil {
			haystack += " " + strings.ToLower(*p.Abstract)
		}
		matched := []string{}
		for _, k := range keywords {
			if strings.Contains(haystack, k) {
				matched = append(matched, k)
			}
			if len(matched) >= 8 {
				break
			}
		}
		resp.Hits = append(resp.Hits, similarityHit{Paper: p, Score: score, MatchedTerms: matched})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "db error: "+err.Error())
		return
	}

	switch {
	case topScore >= 0.15:
		resp.RiskLevel = "high"
	case topScore >= 0.05:
		resp.RiskLevel = "medium"
	}
	resp.TopScore = round4(topScore)
	writeJSON(w, http.StatusOK, resp)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func round4(f float64) float64 {
	return float64(int(f*10000+0.5)) / 10000
}
