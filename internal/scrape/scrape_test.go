package scrape

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const samplePage = `<!DOCTYPE html>
<html lang="id">
<head>
  <title>Judul Halaman Uji</title>
  <meta name="description" content="Deskripsi halaman uji.">
</head>
<body>
  <nav><a href="/home">Home</a> | <a href="/about">About</a></nav>
  <article>
    <h1>Judul Artikel</h1>
    <p>Paragraf pertama berisi konten utama yang cukup panjang supaya
    readability menganggapnya sebagai artikel sungguhan dan tidak
    membuangnya sebagai boilerplate.</p>
    <p>Paragraf kedua dengan <strong>teks tebal</strong> dan sebuah
    <a href="https://example.com">tautan</a> untuk menguji konversi markdown.</p>
  </article>
  <footer>Copyright 2026 — footer yang seharusnya dibuang.</footer>
</body>
</html>`

func newTestServer(t *testing.T, robots string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		if robots == "" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(robots))
	})
	mux.HandleFunc("/artikel", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(samplePage))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestScrapeMainContent(t *testing.T) {
	srv := newTestServer(t, "")
	res, err := Scrape(context.Background(), Options{
		URL:               srv.URL + "/artikel",
		OnlyMainContent:   true,
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, mau 200", res.StatusCode)
	}
	if !strings.Contains(res.Markdown, "Paragraf pertama") {
		t.Errorf("markdown tidak memuat konten utama:\n%s", res.Markdown)
	}
	if !strings.Contains(res.Markdown, "**teks tebal**") {
		t.Errorf("markdown tidak mengonversi <strong>:\n%s", res.Markdown)
	}
	if strings.Contains(res.Markdown, "footer yang seharusnya dibuang") {
		t.Errorf("readability tidak membuang footer:\n%s", res.Markdown)
	}
}

func TestScrapeBlockedByRobots(t *testing.T) {
	srv := newTestServer(t, "User-agent: *\nDisallow: /artikel\n")
	_, err := Scrape(context.Background(), Options{
		URL:               srv.URL + "/artikel",
		AllowPrivateHosts: true,
	})
	if err == nil || !strings.Contains(err.Error(), "robots.txt") {
		t.Fatalf("mau error robots.txt, dapat: %v", err)
	}
}

func TestScrapeRejectsPrivateHost(t *testing.T) {
	srv := newTestServer(t, "")
	_, err := Scrape(context.Background(), Options{URL: srv.URL + "/artikel"})
	if err == nil {
		t.Fatal("mau error anti-SSRF untuk host loopback, dapat nil")
	}
}

func TestScrapeInvalidURL(t *testing.T) {
	for _, u := range []string{"", "ftp://example.com/x", "bukan-url"} {
		if _, err := Scrape(context.Background(), Options{URL: u}); err == nil {
			t.Errorf("mau error untuk URL %q, dapat nil", u)
		}
	}
}
