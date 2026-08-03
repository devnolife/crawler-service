// Package scrape mengimplementasikan scraping on-demand ala Firecrawl:
// request masuk → fetch URL → ekstrak konten utama (readability) →
// konversi ke markdown → langsung return. Tidak menyentuh Postgres.
package scrape

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/PuerkitoBio/goquery"
	readability "github.com/go-shiori/go-readability"
	"github.com/temoto/robotstxt"
)

// UserAgent mengidentifikasi scraper secara sopan.
const UserAgent = "crawler-service-scrape/0.1 (+https://github.com/devnolife/crawler-service)"

// maxBodyBytes membatasi ukuran HTML yang diunduh (10 MB).
const maxBodyBytes = 10 << 20

// Options mengatur satu request scrape.
type Options struct {
	// URL target; wajib http/https.
	URL string
	// OnlyMainContent: true = buang nav/footer/sidebar via readability.
	OnlyMainContent bool
	// Timeout total fetch. Default 30s.
	Timeout time.Duration
	// AllowPrivateHosts mengizinkan target IP privat/loopback.
	// Hanya untuk testing — handler API tidak pernah menyalakannya (anti-SSRF).
	AllowPrivateHosts bool
	// CollectLinks mengisi Result.Links dengan link internal (host sama).
	CollectLinks bool
	// SkipRobots melewati cek robots.txt di sini — dipakai crawl mode
	// yang sudah memuat robots.txt sekali per host.
	SkipRobots bool
	// RenderJS merender halaman lewat browser CDP (Lightpanda/Chrome)
	// sebelum ekstraksi — untuk situs SPA. Butuh Renderer yang tersedia.
	RenderJS bool
	// Renderer dipakai bila RenderJS true. Nil = NewRendererFromEnv().
	Renderer *Renderer
}

// Result adalah hasil scrape satu halaman.
type Result struct {
	URL         string    `json:"url"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	Language    string    `json:"language,omitempty"`
	Markdown    string    `json:"markdown"`
	Links       []string  `json:"links,omitempty"`
	StatusCode  int       `json:"status_code"`
	ScrapedAt   time.Time `json:"scraped_at"`
}

// ErrBlockedByRobots menandakan URL dilarang robots.txt untuk UA kita.
var ErrBlockedByRobots = errors.New("diblok robots.txt")

// Scrape mengambil satu URL dan mengembalikan konten utama sebagai markdown.
func Scrape(ctx context.Context, opts Options) (*Result, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	target, err := url.Parse(opts.URL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return nil, fmt.Errorf("url tidak valid (wajib http/https): %q", opts.URL)
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	client := newClient(opts.AllowPrivateHosts)

	if !opts.SkipRobots {
		if err := checkRobots(ctx, client, target); err != nil {
			return nil, err
		}
	}

	if opts.RenderJS {
		return scrapeRendered(ctx, opts, target)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", target, err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "html") && !strings.Contains(ct, "xml") {
		return nil, fmt.Errorf("content-type tidak didukung: %s", ct)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("baca body: %w", err)
	}

	return buildResult(string(body), target, resp.StatusCode, opts)
}

// scrapeRendered mengambil HTML lewat browser CDP lalu memakai pipeline
// ekstraksi yang sama dengan fetch biasa.
func scrapeRendered(ctx context.Context, opts Options, target *url.URL) (*Result, error) {
	r := opts.Renderer
	if r == nil {
		r = NewRendererFromEnv()
	}
	html, err := r.RenderHTML(ctx, target.String(), opts.AllowPrivateHosts)
	if err != nil {
		return nil, err
	}
	return buildResult(html, target, http.StatusOK, opts)
}

// buildResult menjalankan pipeline ekstraksi: links → readability → markdown.
func buildResult(html string, target *url.URL, statusCode int, opts Options) (*Result, error) {
	res := &Result{
		URL:        target.String(),
		StatusCode: statusCode,
		ScrapedAt:  time.Now().UTC(),
	}

	if opts.CollectLinks {
		res.Links = extractInternalLinks(html, target)
	}
	if opts.OnlyMainContent {
		article, err := readability.FromReader(strings.NewReader(html), target)
		if err == nil && strings.TrimSpace(article.Content) != "" {
			res.Title = article.Title
			res.Description = article.Excerpt
			res.Language = article.Language
			html = article.Content
		}
	}

	md, err := htmltomarkdown.ConvertString(html)
	if err != nil {
		return nil, fmt.Errorf("konversi markdown: %w", err)
	}
	res.Markdown = strings.TrimSpace(md)
	return res, nil
}

// checkRobots memuat robots.txt host target. Gagal ambil (jaringan/4xx)
// dianggap allow — sama seperti perilaku crawler umum.
func checkRobots(ctx context.Context, client *http.Client, target *url.URL) error {
	robotsURL := target.Scheme + "://" + target.Host + "/robots.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	robots, err := robotstxt.FromStatusAndBytes(resp.StatusCode, body)
	if err != nil {
		return nil
	}
	if !robots.FindGroup(UserAgent).Test(target.Path) {
		return fmt.Errorf("%w: %s", ErrBlockedByRobots, target)
	}
	return nil
}

// newClient membuat http.Client dengan guard anti-SSRF: setiap koneksi
// (termasuk setelah redirect) divalidasi agar tidak menuju IP privat,
// loopback, atau link-local. Validasi di level dial mencegah DNS rebinding.
func newClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if !allowPrivate {
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, err
				}
				for _, ip := range ips {
					if isPrivateIP(ip.IP) {
						return nil, fmt.Errorf("target menunjuk ke alamat privat/internal: %s", host)
					}
				}
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("terlalu banyak redirect")
			}
			return nil
		},
	}
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// extractInternalLinks mengambil semua link <a href> satu host dengan base,
// di-resolve jadi absolut, tanpa fragment, terdedup, urutan kemunculan.
func extractInternalLinks(html string, base *url.URL) []string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var links []string
	doc.Find("a[href]").Each(func(_ int, sel *goquery.Selection) {
		href, _ := sel.Attr("href")
		u, err := base.Parse(strings.TrimSpace(href))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return
		}
		if !strings.EqualFold(u.Host, base.Host) {
			return
		}
		u.Fragment = ""
		s := u.String()
		if !seen[s] {
			seen[s] = true
			links = append(links, s)
		}
	})
	return links
}
