package api

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/devnolife/crawler-service/internal/model"
)

var (
	reWord    = regexp.MustCompile(`[A-Za-zÀ-ÿ]{5,}`)
	reNonWord = regexp.MustCompile(`[^A-Za-z0-9À-ÿ]`)
)

var stopwords = map[string]bool{
	"yang": true, "dan": true, "atau": true, "untuk": true, "dengan": true,
	"dalam": true, "pada": true, "adalah": true, "tidak": true, "ini": true,
	"itu": true, "akan": true, "sebagai": true, "dari": true, "oleh": true,
	"tersebut": true, "secara": true, "telah": true, "sangat": true,
	"lebih": true, "namun": true, "tetapi": true, "agar": true, "karena": true,
	"sehingga": true, "their": true, "which": true, "while": true,
	"where": true, "these": true, "those": true, "have": true, "been": true,
	"will": true, "this": true, "that": true, "with": true, "from": true,
	"into": true, "about": true, "than": true, "such": true, "also": true,
	"study": true, "research": true, "penelitian": true, "menggunakan": true,
	"metode": true, "hasil": true,
}

// extractKeywords: token >= 5 huruf, stopwords dibuang, urut frekuensi, top-k.
func extractKeywords(text string, k int) []string {
	words := reWord.FindAllString(strings.ToLower(text), -1)
	freq := map[string]int{}
	order := map[string]int{} // jaga urutan kemunculan untuk tie-break stabil
	for i, w := range words {
		if stopwords[w] {
			continue
		}
		if _, seen := freq[w]; !seen {
			order[w] = i
		}
		freq[w]++
	}
	keys := make([]string, 0, len(freq))
	for w := range freq {
		keys = append(keys, w)
	}
	sort.Slice(keys, func(i, j int) bool {
		if freq[keys[i]] != freq[keys[j]] {
			return freq[keys[i]] > freq[keys[j]]
		}
		return order[keys[i]] < order[keys[j]]
	})
	if len(keys) > k {
		keys = keys[:k]
	}
	return keys
}

// sanitizeKeywords membuang karakter aneh untuk to_tsquery; minimal 4 huruf.
func sanitizeKeywords(keywords []string) []string {
	var safe []string
	for _, k := range keywords {
		if len([]rune(k)) < 4 {
			continue
		}
		s := reNonWord.ReplaceAllString(k, "")
		if s != "" {
			safe = append(safe, s)
		}
	}
	return safe
}

// formatBibTeX membentuk entri @misc dari satu paper.
func formatBibTeX(p model.Paper) string {
	src := p.Source
	if i := strings.LastIndex(src, ":"); i >= 0 {
		src = src[i+1:]
	}
	if i := strings.Index(src, "."); i >= 0 {
		src = src[:i]
	}
	key := strings.ToLower(src + p.SourceID)

	var b strings.Builder
	b.WriteString("@misc{" + key + ",\n")
	b.WriteString("  title  = {" + p.Title + "},\n")
	if len(p.Authors) > 0 {
		b.WriteString("  author = {" + strings.Join(p.Authors, " and ") + "},\n")
	}
	if p.Year != nil {
		b.WriteString("  year   = {" + itoa(*p.Year) + "},\n")
	}
	b.WriteString("  url    = {" + p.URL + "},\n")
	b.WriteString("  note   = {" + p.Source + "},\n")
	b.WriteString("}")
	return b.String()
}

// formatAPA membentuk sitasi gaya APA sederhana.
func formatAPA(p model.Paper) string {
	authors := p.Authors
	if len(authors) == 0 {
		authors = []string{"Anonim"}
	}
	var authorStr string
	if len(authors) > 3 {
		authorStr = authors[0] + " dkk."
	} else {
		authorStr = strings.Join(authors, ", ")
	}
	year := "t.t."
	if p.Year != nil {
		year = itoa(*p.Year)
	}
	return authorStr + " (" + year + "). " + p.Title + ". " + p.Source + ". " + p.URL
}

func itoa(n int) string { return strconv.Itoa(n) }
