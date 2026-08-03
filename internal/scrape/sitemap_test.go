package scrape

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sitemapSite(t *testing.T, withSitemap bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server

	if withSitemap {
		mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "User-agent: *\nAllow: /\nSitemap: %s/sitemap-index.xml\n", srv.URL)
		})
		mux.HandleFunc("/sitemap-index.xml", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<?xml version="1.0"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>%s/sitemap-1.xml</loc></sitemap>
</sitemapindex>`, srv.URL)
		})
		mux.HandleFunc("/sitemap-1.xml", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>%s/artikel/satu</loc></url>
  <url><loc>%s/artikel/dua</loc></url>
  <url><loc>%s/tentang</loc></url>
  <url><loc>https://situs-lain.example/x</loc></url>
</urlset>`, srv.URL, srv.URL, srv.URL)
		})
	} else {
		mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
	}

	page := func(links ...string) string {
		var b strings.Builder
		b.WriteString("<html><body>")
		for _, l := range links {
			fmt.Fprintf(&b, `<a href="%s">x</a>`, l)
		}
		b.WriteString("</body></html>")
		return b.String()
	}
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(page("/a", "/b")))
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(page("/c")))
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(page()))
	})
	mux.HandleFunc("/c", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(page()))
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestMapFromSitemap(t *testing.T) {
	srv := sitemapSite(t, true)
	res, err := Map(context.Background(), MapOptions{
		URL:               srv.URL + "/",
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if res.Source != "sitemap" {
		t.Errorf("source = %s, mau sitemap", res.Source)
	}
	if res.Total != 3 {
		t.Fatalf("total = %d, mau 3 (situs-lain harus tersaring); links = %v", res.Total, res.Links)
	}
	for _, l := range res.Links {
		if strings.Contains(l, "situs-lain") {
			t.Errorf("link eksternal lolos filter: %s", l)
		}
	}
}

func TestMapFallbackCrawl(t *testing.T) {
	srv := sitemapSite(t, false)
	res, err := Map(context.Background(), MapOptions{
		URL:               srv.URL + "/",
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if res.Source != "crawl" {
		t.Errorf("source = %s, mau crawl", res.Source)
	}
	// BFS depth 1: /a, /b dari root; /c dari /a.
	want := map[string]bool{srv.URL + "/a": true, srv.URL + "/b": true, srv.URL + "/c": true}
	for _, l := range res.Links {
		delete(want, l)
	}
	if len(want) != 0 {
		t.Errorf("link hilang dari hasil: %v; dapat %v", want, res.Links)
	}
}

func TestMapSearchAndLimit(t *testing.T) {
	srv := sitemapSite(t, true)
	res, err := Map(context.Background(), MapOptions{
		URL:               srv.URL + "/",
		Search:            "artikel",
		Limit:             1,
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if res.Total != 1 || !strings.Contains(res.Links[0], "artikel") {
		t.Errorf("search+limit gagal: %v", res.Links)
	}
}

func TestMapInvalidURL(t *testing.T) {
	if _, err := Map(context.Background(), MapOptions{URL: "bukan-url"}); err == nil {
		t.Fatal("mau error untuk URL tidak valid")
	}
}
