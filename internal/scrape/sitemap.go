package scrape

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MapOptions mengatur satu request map (link discovery).
type MapOptions struct {
	// URL situs target.
	URL string
	// Limit maksimal URL yang dikembalikan. Default 100, cap 5000.
	Limit int
	// Search memfilter URL yang mengandung substring ini (case-insensitive).
	Search string
	// AllowPrivateHosts hanya untuk testing.
	AllowPrivateHosts bool
}

// MapResult adalah hasil discovery URL sebuah situs.
type MapResult struct {
	URL    string   `json:"url"`
	Source string   `json:"source"` // "sitemap" atau "crawl"
	Total  int      `json:"total"`
	Links  []string `json:"links"`
}

// maxSitemapBytes membatasi ukuran satu file sitemap (20 MB).
const maxSitemapBytes = 20 << 20

// maxSitemapFiles membatasi jumlah file sitemap yang diikuti dari index.
// Toko besar (Shopify dll) memecah katalog ke puluhan file, jadi batas ini
// perlu longgar agar produk di file terakhir tetap terjangkau.
const maxSitemapFiles = 50

// maxSitemapURLs membatasi total URL yang dikumpulkan dari seluruh sitemap.
const maxSitemapURLs = 200_000

// Map menemukan URL internal sebuah situs: coba sitemap.xml dulu
// (termasuk direktif Sitemap: di robots.txt dan sitemap index),
// fallback ke BFS ringan (depth 1, tanpa konversi markdown).
func Map(ctx context.Context, opts MapOptions) (*MapResult, error) {
	target, err := url.Parse(opts.URL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return nil, fmt.Errorf("%w: %q", ErrInvalidURL, opts.URL)
	}
	if opts.Limit <= 0 {
		opts.Limit = 100
	}
	if opts.Limit > 5000 {
		opts.Limit = 5000
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	client := newClient(opts.AllowPrivateHosts)

	res := &MapResult{URL: target.String()}

	links := fromSitemaps(ctx, client, target)
	if len(links) > 0 {
		res.Source = "sitemap"
	} else {
		links = fromShallowCrawl(ctx, client, target, opts)
		res.Source = "crawl"
	}

	links = filterLinks(links, target, opts.Search, opts.Limit)
	res.Links = links
	res.Total = len(links)
	return res, nil
}

// fromSitemaps mengumpulkan URL dari sitemap situs. Kandidat: direktif
// Sitemap: di robots.txt, lalu /sitemap.xml standar.
func fromSitemaps(ctx context.Context, client *http.Client, target *url.URL) []string {
	candidates := sitemapsFromRobots(ctx, client, target)
	candidates = append(candidates, target.Scheme+"://"+target.Host+"/sitemap.xml")

	seen := map[string]bool{}
	var links []string
	fetched := 0
	for len(candidates) > 0 && fetched < maxSitemapFiles && len(links) < maxSitemapURLs {
		smURL := candidates[0]
		candidates = candidates[1:]
		if seen["sm:"+smURL] {
			continue
		}
		seen["sm:"+smURL] = true
		fetched++

		urls, children := fetchSitemap(ctx, client, smURL)
		candidates = append(candidates, children...)
		for _, u := range urls {
			if !seen[u] {
				seen[u] = true
				links = append(links, u)
			}
		}
	}
	return links
}

// sitemapsFromRobots membaca direktif "Sitemap:" dari robots.txt.
func sitemapsFromRobots(ctx context.Context, client *http.Client, target *url.URL) []string {
	body := fetchBytes(ctx, client, target.Scheme+"://"+target.Host+"/robots.txt", 1<<20)
	if body == nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		k, _, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "sitemap") {
			continue
		}
		// Ambil sisa baris setelah "sitemap:" apa adanya supaya skema
		// URL (http://) tidak terpotong di ":" kedua.
		if sm := strings.TrimSpace(line[len(k)+1:]); sm != "" {
			out = append(out, sm)
		}
	}
	return out
}

type sitemapXML struct {
	XMLName xml.Name `xml:""`
	URLs    []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

// fetchSitemap mengunduh dan mem-parse satu sitemap.
// Return: URL konten, plus URL sitemap anak (bila sitemap index).
func fetchSitemap(ctx context.Context, client *http.Client, smURL string) (urls, children []string) {
	body := fetchBytes(ctx, client, smURL, maxSitemapBytes)
	if body == nil {
		return nil, nil
	}
	var sm sitemapXML
	if err := xml.Unmarshal(body, &sm); err != nil {
		return nil, nil
	}
	for _, u := range sm.URLs {
		if loc := strings.TrimSpace(u.Loc); loc != "" {
			urls = append(urls, loc)
		}
	}
	for _, s := range sm.Sitemaps {
		if loc := strings.TrimSpace(s.Loc); loc != "" {
			children = append(children, loc)
		}
	}
	return urls, children
}

// fromShallowCrawl: BFS ringan depth 1 — fetch seed + halaman level 1,
// kumpulkan link internal. Tanpa readability/markdown supaya cepat.
func fromShallowCrawl(ctx context.Context, client *http.Client, target *url.URL, opts MapOptions) []string {
	seen := map[string]bool{target.String(): true}
	var links []string

	pageLinks := fetchPageLinks(ctx, client, target)
	for _, l := range pageLinks {
		if !seen[l] {
			seen[l] = true
			links = append(links, l)
		}
	}

	// Level 1: ikuti sebagian link untuk memperluas peta.
	followed := 0
	for _, l := range pageLinks {
		if followed >= 5 || len(links) >= opts.Limit {
			break
		}
		u, err := url.Parse(l)
		if err != nil {
			continue
		}
		followed++
		for _, l2 := range fetchPageLinks(ctx, client, u) {
			if !seen[l2] {
				seen[l2] = true
				links = append(links, l2)
			}
		}
		select {
		case <-ctx.Done():
			return links
		case <-time.After(500 * time.Millisecond):
		}
	}
	return links
}

// fetchPageLinks mengambil satu halaman HTML dan mengembalikan link internalnya.
func fetchPageLinks(ctx context.Context, client *http.Client, page *url.URL) []string {
	body := fetchBytes(ctx, client, page.String(), maxBodyBytes)
	if body == nil {
		return nil
	}
	return extractInternalLinks(string(body), page)
}

// fetchBytes GET sederhana dengan UA sopan; nil bila gagal/non-2xx.
func fetchBytes(ctx context.Context, client *http.Client, rawURL string, limit int64) []byte {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil
	}
	return body
}

// filterLinks: hanya host sama, filter substring search, potong ke limit.
func filterLinks(links []string, target *url.URL, search string, limit int) []string {
	search = strings.ToLower(strings.TrimSpace(search))
	out := make([]string, 0, len(links))
	for _, l := range links {
		u, err := url.Parse(l)
		if err != nil || !strings.EqualFold(u.Host, target.Host) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(l), search) {
			continue
		}
		out = append(out, l)
		if len(out) >= limit {
			break
		}
	}
	return out
}
