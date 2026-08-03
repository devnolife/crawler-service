package scrape

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newCrawlSite membuat situs kecil: / → /a, /b; /a → /c; /c tanpa link baru.
func newCrawlSite(t *testing.T) *httptest.Server {
	t.Helper()
	page := func(title string, links ...string) string {
		var b strings.Builder
		fmt.Fprintf(&b, "<html><head><title>%s</title></head><body><article><h1>%s</h1>", title, title)
		fmt.Fprintf(&b, "<p>Konten %s yang cukup panjang untuk dianggap artikel oleh readability, berisi kalimat tambahan supaya tidak dibuang.</p>", title)
		for _, l := range links {
			fmt.Fprintf(&b, `<p><a href="%s">link ke %s</a></p>`, l, l)
		}
		b.WriteString("</article></body></html>")
		return b.String()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: *\nDisallow: /rahasia\n"))
	})
	serve := func(path, html string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(html))
		})
	}
	serve("/{$}", page("Root", "/a", "/b", "/rahasia", "https://situs-lain.example/x"))
	serve("/a", page("Halaman A", "/c"))
	serve("/b", page("Halaman B"))
	serve("/c", page("Halaman C"))
	serve("/rahasia", page("Rahasia"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func waitJob(t *testing.T, m *Manager, id string) *CrawlJob {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		job := m.Get(id)
		if job == nil {
			t.Fatal("job hilang dari manager")
		}
		if job.Status == StatusCompleted || job.Status == StatusFailed {
			return job
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("job tidak selesai dalam 30s")
	return nil
}

func TestCrawlBFS(t *testing.T) {
	srv := newCrawlSite(t)
	m := NewManager(1)
	job, err := m.Start(CrawlOptions{
		URL:               srv.URL + "/",
		MaxPages:          10,
		MaxDepth:          2,
		Delay:             500 * time.Millisecond,
		OnlyMainContent:   true,
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := waitJob(t, m, job.ID)
	if done.Status != StatusCompleted {
		t.Fatalf("status = %s, error = %s", done.Status, done.Error)
	}
	// Root + A + B + C (rahasia diblok robots, situs lain di-skip).
	if done.Total != 4 {
		urls := make([]string, 0, len(done.Pages))
		for _, p := range done.Pages {
			urls = append(urls, p.URL)
		}
		t.Fatalf("total = %d, mau 4; pages: %v; errors: %v", done.Total, urls, done.Errors)
	}
	if len(done.Errors) != 1 || !strings.Contains(done.Errors[0].Error, "robots") {
		t.Errorf("mau 1 error robots utk /rahasia, dapat: %v", done.Errors)
	}
	for _, p := range done.Pages {
		if strings.Contains(p.URL, "situs-lain") {
			t.Errorf("link eksternal ikut ter-crawl: %s", p.URL)
		}
		if p.Markdown == "" {
			t.Errorf("markdown kosong untuk %s", p.URL)
		}
	}
}

func TestCrawlMaxPages(t *testing.T) {
	srv := newCrawlSite(t)
	m := NewManager(1)
	var mu sync.Mutex
	var persisted []string
	job, err := m.Start(CrawlOptions{
		URL:      srv.URL + "/",
		MaxPages: 2,
		MaxDepth: 2,
		Delay:    500 * time.Millisecond,
		OnPage: func(r *Result) {
			mu.Lock()
			persisted = append(persisted, r.URL)
			mu.Unlock()
		},
		AllowPrivateHosts: true,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := waitJob(t, m, job.ID)
	if done.Total != 2 {
		t.Fatalf("total = %d, mau 2 (max_pages)", done.Total)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(persisted) != 2 {
		t.Errorf("OnPage terpanggil %d kali, mau 2: %v", len(persisted), persisted)
	}
}

func TestCrawlInvalidURL(t *testing.T) {
	m := NewManager(1)
	if _, err := m.Start(CrawlOptions{URL: "ftp://x"}); err == nil {
		t.Fatal("mau error untuk URL ftp, dapat nil")
	}
}

func TestManagerGetUnknown(t *testing.T) {
	m := NewManager(1)
	if job := m.Get("tidak-ada"); job != nil {
		t.Fatalf("mau nil untuk job tak dikenal, dapat %+v", job)
	}
}
