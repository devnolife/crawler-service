package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devnolife/crawler-service/internal/model"
)

func TestRunAgainstFakeEprints(t *testing.T) {
	mux := http.NewServeMux()
	var base string

	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "User-agent: *\nDisallow: /private/\n")
	})
	mux.HandleFunc("/cgi/search/simple", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("_offset") == "" {
			fmt.Fprintf(w, `<html><body>
<p class="ep_search_result"><a href="%s/id/eprint/101">Rec 101</a></p>
<p class="ep_search_result"><a href="%s/id/eprint/102">Rec 102</a></p>
<a href="%s/cgi/search/simple?_offset=20&search_offset=20">2</a>
</body></html>`, base, base, base)
			return
		}
		// Halaman 2: satu record lagi.
		fmt.Fprintf(w, `<html><body>
<p class="ep_search_result"><a href="%s/id/eprint/103">Rec 103</a></p>
</body></html>`, base)
	})
	mux.HandleFunc("/id/eprint/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><head>
<meta name="DC.title" content="Judul %s" />
<meta name="DC.creator" content="Sari, Dewi" />
<meta name="DC.creator" content="Putra, Adi" />
<meta name="DC.date" content="2021-06-01" />
<meta name="DC.publisher" content="Universitas Test" />
<meta name="DC.subject" content="ML; AI" />
<meta name="DC.identifier" content="%s/files/paper.pdf" />
</head><body>
<h2>Abstract</h2><p>Ini abstrak pengujian.</p>
</body></html>`, r.URL.Path, base)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := New(ctx, Config{
		BaseURL:  srv.URL,
		Query:    "machine learning",
		MaxPages: 2,
		Delay:    time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var papers []model.Paper
	if err := c.Run(ctx, func(p model.Paper) error {
		papers = append(papers, p)
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(papers) != 3 {
		t.Fatalf("papers = %d, want 3", len(papers))
	}
	p := papers[0]
	if p.SourceID != "101" {
		t.Errorf("SourceID = %q", p.SourceID)
	}
	if p.Title != "Judul /id/eprint/101" {
		t.Errorf("Title = %q", p.Title)
	}
	if len(p.Authors) != 2 || p.Authors[0] != "Sari, Dewi" {
		t.Errorf("Authors = %v", p.Authors)
	}
	if p.Year == nil || *p.Year != 2021 {
		t.Errorf("Year = %v", p.Year)
	}
	if p.Abstract == nil || *p.Abstract != "Ini abstrak pengujian." {
		t.Errorf("Abstract = %v", p.Abstract)
	}
	if len(p.Keywords) != 2 || p.Keywords[0] != "ML" {
		t.Errorf("Keywords = %v", p.Keywords)
	}
	if len(p.DatasetURLs) != 1 || p.HasDataset == nil || !*p.HasDataset {
		t.Errorf("DatasetURLs = %v HasDataset = %v", p.DatasetURLs, p.HasDataset)
	}
}

func TestRobotsBlocked(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "User-agent: *\nDisallow: /cgi/\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	c, err := New(ctx, Config{BaseURL: srv.URL, Query: "x", MaxPages: 1, Delay: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = c.Run(ctx, func(model.Paper) error { return nil })
	if err == nil {
		t.Fatal("Run harus gagal karena robots.txt memblok /cgi/")
	}
}
