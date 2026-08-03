package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/devnolife/crawler-service/internal/model"
)

func TestParseAPIKeys(t *testing.T) {
	got := parseAPIKeys("key1:studio-revisi, key2:wizard-research,key3")
	want := map[string]string{
		"key1": "studio-revisi",
		"key2": "wizard-research",
		"key3": "default",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseAPIKeys = %v, want %v", got, want)
	}
	if len(parseAPIKeys("")) != 0 {
		t.Error("string kosong harus menghasilkan map kosong (auth mati)")
	}
}

func TestMiddlewareAuthAndPublic(t *testing.T) {
	s := New(nil, nil)
	s.keys = map[string]string{"rahasia": "studio-revisi"}

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := s.middleware(inner)

	// Tanpa key → 401.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/datasets/search", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("tanpa key: status = %d, want 401", rr.Code)
	}

	// Key benar → 200 + X-Client.
	req := httptest.NewRequest("GET", "/api/v1/datasets/search", nil)
	req.Header.Set("X-API-Key", "rahasia")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("key benar: status = %d, want 200", rr.Code)
	}
	if rr.Header().Get("X-Client") != "studio-revisi" {
		t.Errorf("X-Client = %q", rr.Header().Get("X-Client"))
	}

	// /health publik.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/health", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("/health: status = %d, want 200", rr.Code)
	}
}

func TestRateLimit(t *testing.T) {
	s := New(nil, nil)
	s.limit = 3
	for i := 0; i < 3; i++ {
		if !s.allow("c") {
			t.Fatalf("request %d harus lolos", i+1)
		}
	}
	if s.allow("c") {
		t.Error("request ke-4 harus diblok")
	}
	if !s.allow("lain") {
		t.Error("client lain tidak boleh terdampak")
	}
}

func TestExtractKeywords(t *testing.T) {
	got := extractKeywords(
		"Penelitian machine learning untuk deteksi plagiarisme menggunakan machine learning modern", 3)
	if len(got) == 0 || got[0] != "machine" && got[0] != "learning" {
		t.Errorf("extractKeywords = %v", got)
	}
	for _, w := range got {
		if stopwords[w] {
			t.Errorf("stopword %q ikut terpilih", w)
		}
	}
}

func TestFormatCitations(t *testing.T) {
	year := 2021
	p := model.Paper{
		Source:   "eprints:eprints.ums.ac.id",
		SourceID: "59935",
		Title:    "Judul Uji",
		Authors:  []string{"Sari, Dewi", "Putra, Adi"},
		Year:     &year,
		URL:      "https://eprints.ums.ac.id/id/eprint/59935",
	}
	bib := formatBibTeX(p)
	if want := "@misc{eprints59935,"; bib[:len(want)] != want {
		t.Errorf("bibtex key salah: %q", bib)
	}
	apa := formatAPA(p)
	if want := "Sari, Dewi, Putra, Adi (2021). Judul Uji. eprints:eprints.ums.ac.id. https://eprints.ums.ac.id/id/eprint/59935"; apa != want {
		t.Errorf("apa = %q", apa)
	}
}
