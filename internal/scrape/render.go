package scrape

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// Renderer merender halaman lewat browser headless eksternal via CDP
// (Chrome DevTools Protocol). Didesain untuk Lightpanda
// (https://github.com/lightpanda-io/browser) tetapi kompatibel dengan
// browser CDP apa pun (Chrome/Chromium headless, browserless, dll).
//
// Konfigurasi: env CRAWLER_CDP_URL, contoh "ws://127.0.0.1:9222".
// Kosong = JS rendering tidak tersedia.
type Renderer struct {
	cdpURL string
}

// ErrRenderUnavailable menandakan CRAWLER_CDP_URL tidak dikonfigurasi.
var ErrRenderUnavailable = errors.New("render_js tidak tersedia: set CRAWLER_CDP_URL ke endpoint CDP (mis. ws://127.0.0.1:9222 untuk Lightpanda)")

// renderTimeout adalah batas default satu render.
const renderTimeout = 20 * time.Second

// NewRendererFromEnv membaca CRAWLER_CDP_URL. Selalu mengembalikan Renderer;
// Available() false bila env kosong.
func NewRendererFromEnv() *Renderer {
	return &Renderer{cdpURL: strings.TrimSpace(os.Getenv("CRAWLER_CDP_URL"))}
}

// Available melaporkan apakah JS rendering dikonfigurasi.
func (r *Renderer) Available() bool { return r.cdpURL != "" }

// RenderHTML membuka url di browser CDP, menunggu dokumen siap, dan
// mengembalikan outerHTML hasil eksekusi JavaScript.
func (r *Renderer) RenderHTML(ctx context.Context, rawURL string, allowPrivate bool) (string, error) {
	if !r.Available() {
		return "", ErrRenderUnavailable
	}
	target, err := url.Parse(rawURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return "", fmt.Errorf("%w: %q", ErrInvalidURL, rawURL)
	}
	// Anti-SSRF: browser melakukan fetch sendiri sehingga guard dial kita
	// tidak berlaku — validasi resolusi DNS target sebelum menyerahkan URL.
	if !allowPrivate {
		if err := checkPublicHost(ctx, target.Hostname()); err != nil {
			return "", err
		}
	}

	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()

	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(ctx, r.cdpURL)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	var html string
	err = chromedp.Run(browserCtx,
		chromedp.Navigate(target.String()),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	)
	if err != nil {
		return "", fmt.Errorf("render %s via CDP: %w", target, err)
	}
	return html, nil
}

// checkPublicHost memastikan hostname tidak me-resolve ke IP privat/loopback.
func checkPublicHost(ctx context.Context, hostname string) error {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", hostname, err)
	}
	for _, ip := range ips {
		if isPrivateIP(ip.IP) {
			return fmt.Errorf("target menunjuk ke alamat privat/internal: %s", hostname)
		}
	}
	return nil
}
