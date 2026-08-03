// Package crawler mengimplementasikan crawler generik EPrints.
//
// Repositori universitas Indonesia hampir semuanya memakai EPrints (UMS, UPI,
// perpustakaan UI, ITB, ITS, UGM, dll). Halamannya mengekspos meta tag Dublin
// Core sehingga mudah di-scrape.
package crawler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/temoto/robotstxt"

	"github.com/devnolife/crawler-service/internal/model"
)

const (
	// UserAgent mengidentifikasi crawler secara sopan.
	UserAgent = "revisi-studio-crawler/0.2 (+https://revisi-studio.id)"
	// pageSize adalah jumlah hasil per halaman search EPrints.
	pageSize = 20
)

var (
	reEprintID   = regexp.MustCompile(`/eprint/(\d+)`)
	reYear       = regexp.MustCompile(`(19|20)\d{2}`)
	reRecordLink = regexp.MustCompile(`https?://[^"]+/id/eprint/\d+/?$`)
	reOffset     = regexp.MustCompile(`search_offset=(\d+)`)
)

// Config berisi parameter satu sesi crawl EPrints.
type Config struct {
	BaseURL  string        // contoh: https://eprints.ums.ac.id
	Query    string        // kata kunci pencarian
	MaxPages int           // maksimal halaman search yang diikuti
	Delay    time.Duration // jeda antar-request (politeness)
	Logger   *slog.Logger
}

// Crawler menjalankan crawl EPrints: search → record → Paper.
type Crawler struct {
	cfg    Config
	client *http.Client
	robots *robotstxt.RobotsData
	host   string
	log    *slog.Logger
}

// New membuat Crawler dan memuat robots.txt dari host target.
func New(ctx context.Context, cfg Config) (*Crawler, error) {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = 2
	}
	if cfg.Delay <= 0 {
		cfg.Delay = 1500 * time.Millisecond
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("base_url tidak valid: %q", cfg.BaseURL)
	}

	c := &Crawler{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
		host:   u.Host,
		log:    cfg.Logger,
	}
	if err := c.loadRobots(ctx); err != nil {
		// Gagal ambil robots.txt bukan alasan berhenti; anggap allow-all
		// hanya bila servernya memang tidak menyediakan (4xx). Error jaringan
		// tetap dilaporkan supaya tidak crawl buta.
		return nil, err
	}
	return c, nil
}

func (c *Crawler) loadRobots(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+"/robots.txt", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("ambil robots.txt: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	c.robots, err = robotstxt.FromStatusAndBytes(resp.StatusCode, body)
	if err != nil {
		return fmt.Errorf("parse robots.txt: %w", err)
	}
	return nil
}

func (c *Crawler) allowed(rawURL string) bool {
	if c.robots == nil {
		return true
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	// Uji path+query: banyak situs melarang berdasarkan query string.
	path := u.Path
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return c.robots.FindGroup(UserAgent).Test(path)
}

// fetch mengambil satu URL dengan politeness delay + retry ringan.
func (c *Crawler) fetch(ctx context.Context, rawURL string) (*goquery.Document, error) {
	if !c.allowed(rawURL) {
		return nil, fmt.Errorf("diblok robots.txt: %s", rawURL)
	}

	var lastErr error
	for attempt := 0; attempt <= 2; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.cfg.Delay):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", UserAgent)

		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, rawURL)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, rawURL)
		}
		doc, err := goquery.NewDocumentFromReader(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		return doc, nil
	}
	return nil, fmt.Errorf("gagal setelah retry: %w", lastErr)
}

// searchURL membangun URL "simple search" EPrints; offset paginasi kelipatan 20.
func (c *Crawler) searchURL(offset int) string {
	q := url.Values{
		"q":              {c.cfg.Query},
		"_action_search": {"Search"},
		"_order":         {"bytitle"},
		"basic_srchtype": {"ALL"},
		"_satisfyall":    {"ALL"},
	}
	if offset > 0 {
		q.Set("_offset", strconv.Itoa(offset))
	}
	return c.cfg.BaseURL + "/cgi/search/simple?" + q.Encode()
}

// Run menjalankan crawl penuh dan mengirim setiap Paper ke out.
func (c *Crawler) Run(ctx context.Context, out func(model.Paper) error) error {
	pageURL := c.searchURL(0)
	seen := map[string]bool{}

	for page := 1; page <= c.cfg.MaxPages; page++ {
		doc, err := c.fetch(ctx, pageURL)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			return fmt.Errorf("halaman search %d: %w", page, err)
		}

		links := c.recordLinks(doc)
		c.log.Info("halaman search", "page", page, "records", len(links))
		if len(links) == 0 {
			break
		}

		for _, href := range links {
			abs := c.absolute(pageURL, href)
			if abs == "" || seen[abs] {
				continue
			}
			seen[abs] = true

			paper, err := c.parseRecord(ctx, abs)
			if err != nil {
				c.log.Warn("gagal parse record", "url", abs, "err", err)
				continue
			}
			if err := out(paper); err != nil {
				return err
			}
		}

		if page == c.cfg.MaxPages {
			break
		}
		pageURL = c.nextPageURL(doc, pageURL, page*pageSize)
	}
	return nil
}

// recordLinks mengambil link record /id/eprint/NNN dari halaman search.
func (c *Crawler) recordLinks(doc *goquery.Document) []string {
	var links []string
	doc.Find(`p.ep_search_result a[href*="/id/eprint/"]`).Each(func(_ int, s *goquery.Selection) {
		if href, ok := s.Attr("href"); ok {
			links = append(links, href)
		}
	})
	if len(links) > 0 {
		return links
	}
	// Fallback: beberapa tema EPrints tidak memakai wrapper class.
	doc.Find(`a[href*="/id/eprint/"]`).Each(func(_ int, s *goquery.Selection) {
		if href, ok := s.Attr("href"); ok && reRecordLink.MatchString(href) {
			links = append(links, href)
		}
	})
	return links
}

// nextPageURL mencari link paginasi asli (search_offset=N). EPrints meng-cache
// hasil search server-side; beberapa repo (mis. digilib.uin-suka.ac.id)
// mengabaikan `_offset` buatan sendiri dan menyajikan ulang halaman 1, jadi
// utamakan link paginasi yang benar-benar dirender halaman. Fallback ke URL
// `_offset` legacy untuk tema lama.
func (c *Crawler) nextPageURL(doc *goquery.Document, base string, nextOffset int) string {
	var found string
	doc.Find(`a[href*="search_offset="]`).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		href, ok := s.Attr("href")
		if !ok {
			return true
		}
		m := reOffset.FindStringSubmatch(href)
		if m == nil {
			return true
		}
		if n, _ := strconv.Atoi(m[1]); n == nextOffset {
			found = c.absolute(base, href)
			return false
		}
		return true
	})
	if found != "" {
		return found
	}
	return c.searchURL(nextOffset)
}

func (c *Crawler) absolute(base, href string) string {
	bu, err := url.Parse(base)
	if err != nil {
		return ""
	}
	hu, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return ""
	}
	abs := bu.ResolveReference(hu)
	if abs.Host != c.host {
		return ""
	}
	return abs.String()
}

// parseRecord mengambil halaman record dan mengekstrak metadata Dublin Core.
func (c *Crawler) parseRecord(ctx context.Context, recordURL string) (model.Paper, error) {
	doc, err := c.fetch(ctx, recordURL)
	if err != nil {
		return model.Paper{}, err
	}

	meta := collectMeta(doc)

	title := strings.TrimSpace(meta.first("DC.title"))
	var authors []string
	for _, a := range meta.all("DC.creator") {
		if v := strings.Trim(a, " ,"); v != "" {
			authors = append(authors, v)
		}
	}

	journal := meta.first("DC.publisher")
	if journal == "" {
		journal = meta.first("eprints.publisher")
	}

	// Abstract biasanya di <h2>Abstract</h2> + <p> setelahnya.
	abstract := extractAbstract(doc)

	// Link PDF diekspos lewat DC.identifier.
	var pdfURLs []string
	for _, id := range meta.all("DC.identifier") {
		if strings.HasSuffix(strings.ToLower(id), ".pdf") {
			pdfURLs = append(pdfURLs, c.absolute(recordURL, id))
		}
	}
	hasPDF := len(pdfURLs) > 0

	p := model.Paper{
		Source:      "eprints:" + c.host,
		SourceID:    extractEprintID(recordURL),
		URL:         recordURL,
		Title:       title,
		Authors:     authors,
		Keywords:    splitSubjects(meta.all("DC.subject")),
		DatasetURLs: pdfURLs,
		ScrapedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if journal != "" {
		p.Journal = &journal
	}
	if abstract != "" {
		p.Abstract = &abstract
	}
	if y := parseYear(meta.first("DC.date")); y != nil {
		p.Year = y
	}
	p.HasDataset = &hasPDF
	// PDF EPrints umumnya open access.
	p.IsOpenAccess = &hasPDF
	if p.Authors == nil {
		p.Authors = []string{}
	}
	if p.Keywords == nil {
		p.Keywords = []string{}
	}
	if p.DatasetURLs == nil {
		p.DatasetURLs = []string{}
	}
	return p, nil
}

func extractAbstract(doc *goquery.Document) string {
	var abstract string
	doc.Find("h2").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		heading := strings.ToUpper(strings.TrimSpace(s.Text()))
		if heading == "ABSTRACT" || heading == "ABSTRAK" {
			abstract = strings.TrimSpace(s.NextFiltered("p").Text())
			return abstract == ""
		}
		return true
	})
	if abstract == "" {
		abstract = strings.TrimSpace(doc.Find("div.abstract p").First().Text())
	}
	return abstract
}

func extractEprintID(rawURL string) string {
	if m := reEprintID.FindStringSubmatch(rawURL); m != nil {
		return m[1]
	}
	return ""
}

func parseYear(raw string) *int {
	if raw == "" {
		return nil
	}
	if m := reYear.FindString(raw); m != "" {
		if y, err := strconv.Atoi(m); err == nil {
			return &y
		}
	}
	return nil
}

func splitSubjects(raws []string) []string {
	var subjects []string
	for _, r := range raws {
		for _, s := range strings.Split(r, ";") {
			if s = strings.TrimSpace(s); s != "" {
				subjects = append(subjects, s)
			}
		}
	}
	return subjects
}

// metaBag adalah multimap sederhana untuk tag <meta> (DC.creator bisa berulang).
type metaBag map[string][]string

func collectMeta(doc *goquery.Document) metaBag {
	bag := metaBag{}
	doc.Find("meta").Each(func(_ int, s *goquery.Selection) {
		name, _ := s.Attr("name")
		content, _ := s.Attr("content")
		if name != "" {
			bag[name] = append(bag[name], content)
		}
	})
	return bag
}

func (b metaBag) first(key string) string {
	if vs := b[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

func (b metaBag) all(key string) []string { return b[key] }
