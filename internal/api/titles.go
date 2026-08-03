package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// ------------------------------------------ /api/v1/research/title-suggest

type titleSuggestion struct {
	Title           string  `json:"title"`
	Rationale       string  `json:"rationale"`
	MethodologyHint *string `json:"methodology_hint"`
}

type titleGenResponse struct {
	Topic        string            `json:"topic"`
	KeywordsUsed []string          `json:"keywords_used"`
	ContextCount int               `json:"context_count"`
	Suggestions  []titleSuggestion `json:"suggestions"`
}

type contextPaper struct {
	Title string
	Year  *int
}

// fetchContextPapers mengambil k paper teratas yang relevan dengan topik.
//
// Degradasi anggun: bila DB korpus belum di-setup / tidak bisa diakses,
// kembalikan list kosong supaya generator judul tetap jalan (mode tanpa
// konteks, mengandalkan LLM saja) alih-alih 500.
func (s *Server) fetchContextPapers(ctx context.Context, topic string, k int) []contextPaper {
	keywords := extractKeywords(topic, 6)
	safe := sanitizeKeywords(keywords)
	if len(safe) == 0 {
		return nil
	}
	tsQuery := strings.Join(safe, " | ")
	sql := `SELECT title, year
          FROM papers
         WHERE to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,''))
               @@ to_tsquery('simple', $1)
         ORDER BY ts_rank(to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(abstract,'')),
                          to_tsquery('simple', $1)) DESC,
                  year DESC NULLS LAST
         LIMIT $2`
	rows, err := s.pool.Query(ctx, sql, tsQuery, k)
	if err != nil {
		s.log.Warn("title-suggest: lewati konteks paper (DB tak tersedia)", "err", err)
		return nil
	}
	defer rows.Close()
	var papers []contextPaper
	for rows.Next() {
		var p contextPaper
		if err := rows.Scan(&p.Title, &p.Year); err != nil {
			return papers
		}
		papers = append(papers, p)
	}
	return papers
}

var reJSONBlock = regexp.MustCompile(`(?s)\{.*\}`)

// handleTitleSuggest: generate calon judul skripsi berbasis topik + konteks
// paper dari DB, via Ollama.
func (s *Server) handleTitleSuggest(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Topic   string `json:"topic"`
		Program string `json:"program"`
		N       int    `json:"n"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "body JSON tidak valid")
		return
	}
	topic := strings.TrimSpace(payload.Topic)
	if len(topic) < 5 {
		writeError(w, http.StatusBadRequest, "topic minimal 5 karakter")
		return
	}
	n := payload.N
	if n < 1 || n > 10 {
		n = 5
	}

	contextPapers := s.fetchContextPapers(r.Context(), topic, 8)
	keywords := extractKeywords(topic, 6)

	var contextBlock, gapInstr string
	if len(contextPapers) > 0 {
		var lines []string
		for _, p := range contextPapers {
			year := "t.t."
			if p.Year != nil {
				year = itoa(*p.Year)
			}
			lines = append(lines, fmt.Sprintf("- (%s) %s", year, strings.TrimSpace(p.Title)))
		}
		contextBlock = "Berikut paper-paper yang sudah ada di topik serupa:\n" + strings.Join(lines, "\n")
		gapInstr = "Buatkan judul yang BERBEDA dari yang sudah ada di atas — cari celah penelitian (gap), " +
			"metode baru, atau sudut pandang yang belum dibahas."
	} else {
		contextBlock = "Belum ada paper serupa di koleksi internal."
		gapInstr = "Buat judul yang spesifik dan dapat dieksekusi."
	}

	progLine := ""
	if p := strings.TrimSpace(payload.Program); p != "" {
		progLine = "Program studi: " + p + "\n"
	}

	prompt := fmt.Sprintf(`Anda adalah dosen pembimbing skripsi berpengalaman. Bantu mahasiswa merumuskan calon judul skripsi yang spesifik, fokus, dan layak dikerjakan dalam 1 semester.

Topik mahasiswa: %s
%s%s

%s

ANATOMI judul skripsi yang baik (ikuti polanya):
[metode/pendekatan] + [variabel/objek utama] + [kata relasi: pengaruh/hubungan/terhadap] + [variabel terikat] + [konteks objek/populasi/lokasi: "pada ... di ..."]

ATURAN MUTU (wajib dipatuhi tiap judul):
- panjang 8-20 kata, spesifik & terukur (hindari judul terlalu umum)
- gunakan Huruf Kapital di Awal Tiap Kata (Title Case)
- ada kata relasi penelitian (pengaruh/hubungan/analisis/implementasi/perbandingan)
- ada konteks objek/lokasi penelitian (mis. "pada Siswa SMA", "di Kota Makassar")
- hindari kata kabur: beberapa, suatu, berbagai, tentang, mengenai
- jangan menyalin judul paper yang sudah ada — cari sudut/gap baru

CONTOH judul kuat (tiru POLA & MUTUnya, bukan isinya):
"Pengaruh Model Pembelajaran Problem Based Learning terhadap Kemampuan Berpikir Kritis Siswa pada Mata Pelajaran IPA di SMP Negeri 1 Makassar"

Hasilkan TEPAT %d calon judul. Untuk tiap judul beri: alasan singkat (gap/keunggulan) dan saran metode/dataset/teknik analisis.

WAJIB output JSON valid berikut, tanpa teks tambahan apa pun:
{
  "suggestions": [
    {"title": "judul lengkap", "rationale": "alasan singkat 1 kalimat", "methodology_hint": "saran metode/dataset/analisis 1 kalimat"}
  ]
}
`, topic, progLine, contextBlock, gapInstr, n)

	raw, err := s.llm.Generate(r.Context(), prompt, 0.7, 1100)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	suggestions := parseTitleSuggestions(raw, n)

	writeJSON(w, http.StatusOK, titleGenResponse{
		Topic:        topic,
		KeywordsUsed: keywords,
		ContextCount: len(contextPapers),
		Suggestions:  suggestions,
	})
}

// parseTitleSuggestions mem-parse JSON dari respons LLM; model bisa membungkus
// output dalam ```json ... ```. Fallback: parser line-based.
func parseTitleSuggestions(raw string, n int) []titleSuggestion {
	suggestions := []titleSuggestion{}

	if m := reJSONBlock.FindString(raw); m != "" {
		var data struct {
			Suggestions []struct {
				Title           string `json:"title"`
				Rationale       string `json:"rationale"`
				MethodologyHint string `json:"methodology_hint"`
			} `json:"suggestions"`
		}
		if err := json.Unmarshal([]byte(m), &data); err == nil {
			for _, sug := range data.Suggestions {
				if len(suggestions) >= n {
					break
				}
				title := strings.TrimSpace(sug.Title)
				if title == "" {
					continue
				}
				rationale := strings.TrimSpace(sug.Rationale)
				if rationale == "" {
					rationale = "—"
				}
				ts := titleSuggestion{Title: title, Rationale: rationale}
				if method := strings.TrimSpace(sug.MethodologyHint); method != "" {
					ts.MethodologyHint = &method
				}
				suggestions = append(suggestions, ts)
			}
		}
	}

	if len(suggestions) == 0 {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.Trim(line, " -*•1234567890.")
			if l := len([]rune(line)); l >= 15 && l <= 250 {
				suggestions = append(suggestions, titleSuggestion{Title: line, Rationale: "—"})
			}
			if len(suggestions) >= n {
				break
			}
		}
	}
	return suggestions
}
