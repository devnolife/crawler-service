// Proxy rotation: pakai daftar proxy dari env CRAWLER_PROXY_URLS
// (dipisah koma; skema http, https, atau socks5). Setiap request scrape
// memakai proxy berikutnya secara round-robin. Kosong = koneksi langsung.
//
// Catatan: ini BUKAN jaringan anti-bot — kualitas tergantung proxy yang
// kamu sediakan (datacenter/residential dari provider mana pun).
package scrape

import (
	"net/url"
	"os"
	"strings"
	"sync/atomic"
)

var proxyCounter atomic.Uint64

// proxyList mem-parse CRAWLER_PROXY_URLS setiap dipanggil (murah, jarang).
func proxyList() []*url.URL {
	raw := strings.TrimSpace(os.Getenv("CRAWLER_PROXY_URLS"))
	if raw == "" {
		return nil
	}
	var out []*url.URL
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		u, err := url.Parse(part)
		if err != nil || u.Host == "" {
			continue
		}
		switch u.Scheme {
		case "http", "https", "socks5":
			out = append(out, u)
		}
	}
	return out
}

// nextProxy mengembalikan proxy berikutnya (round-robin), nil bila tanpa proxy.
func nextProxy() *url.URL {
	proxies := proxyList()
	if len(proxies) == 0 {
		return nil
	}
	n := proxyCounter.Add(1)
	return proxies[(n-1)%uint64(len(proxies))]
}

// proxyConfigured melaporkan apakah rotasi proxy aktif.
func proxyConfigured() bool { return len(proxyList()) > 0 }
