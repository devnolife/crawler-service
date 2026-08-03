package scrape

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProxyListParsing(t *testing.T) {
	t.Setenv("CRAWLER_PROXY_URLS", "http://p1:8080, socks5://p2:1080 ,, ftp://buruk:1 , bukan url")
	got := proxyList()
	if len(got) != 2 {
		t.Fatalf("proxyList = %v, mau 2 entri valid", got)
	}
	if got[0].Scheme != "http" || got[1].Scheme != "socks5" {
		t.Errorf("skema = %s, %s", got[0].Scheme, got[1].Scheme)
	}

	t.Setenv("CRAWLER_PROXY_URLS", "")
	if proxyConfigured() {
		t.Error("env kosong harus berarti proxy mati")
	}
}

func TestProxyRoundRobinFetch(t *testing.T) {
	// Halaman target.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><p>lewat proxy</p></body></html>")
	}))
	t.Cleanup(origin.Close)

	// Dua forward proxy sederhana yang menghitung request.
	var hits1, hits2 atomic.Int64
	mkProxy := func(hits *atomic.Int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			// Forward proxy HTTP: request URI absolut.
			resp, err := http.DefaultTransport.RoundTrip(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()
			for k, vv := range resp.Header {
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(resp.StatusCode)
			buf := make([]byte, 32*1024)
			for {
				n, err := resp.Body.Read(buf)
				if n > 0 {
					w.Write(buf[:n])
				}
				if err != nil {
					return
				}
			}
		}))
	}
	p1 := mkProxy(&hits1)
	p2 := mkProxy(&hits2)
	t.Cleanup(p1.Close)
	t.Cleanup(p2.Close)

	t.Setenv("CRAWLER_PROXY_URLS", p1.URL+","+p2.URL)

	// Dua scrape → masing-masing proxy kena satu kali (round-robin).
	for i := 0; i < 2; i++ {
		res, err := Scrape(context.Background(), Options{
			URL:               origin.URL + "/halaman",
			SkipRobots:        true,
			AllowPrivateHosts: true,
		})
		if err != nil {
			t.Fatalf("Scrape #%d: %v", i+1, err)
		}
		if !strings.Contains(res.Markdown, "lewat proxy") {
			t.Errorf("markdown = %q", res.Markdown)
		}
	}
	if hits1.Load() == 0 || hits2.Load() == 0 {
		t.Errorf("rotasi tidak jalan: proxy1=%d proxy2=%d", hits1.Load(), hits2.Load())
	}
}

func TestProxyRetryOnBlocked(t *testing.T) {
	// Origin selalu 403 → dengan 2 proxy, fetch dicoba 2x lalu gagal jelas.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(origin.Close)

	var attempts atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
	}))
	t.Cleanup(proxy.Close)

	t.Setenv("CRAWLER_PROXY_URLS", proxy.URL)

	_, err := Scrape(context.Background(), Options{
		URL:               origin.URL + "/x",
		SkipRobots:        true,
		AllowPrivateHosts: true,
	})
	if err == nil || !strings.Contains(err.Error(), "diblokir") {
		t.Fatalf("mau error diblokir, dapat: %v", err)
	}
	if attempts.Load() != 2 {
		t.Errorf("attempts = %d, mau 2 (retry sekali)", attempts.Load())
	}
}
