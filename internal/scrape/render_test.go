package scrape

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestRendererUnavailable(t *testing.T) {
	r := &Renderer{}
	if r.Available() {
		t.Fatal("renderer tanpa CDP URL harus tidak tersedia")
	}
	_, err := r.RenderHTML(context.Background(), "https://example.com", true)
	if !errors.Is(err, ErrRenderUnavailable) {
		t.Fatalf("mau ErrRenderUnavailable, dapat: %v", err)
	}
}

func TestRendererInvalidURL(t *testing.T) {
	r := &Renderer{cdpURLs: []string{"ws://127.0.0.1:9222"}}
	if _, err := r.RenderHTML(context.Background(), "bukan-url", true); err == nil {
		t.Fatal("mau error untuk URL tidak valid")
	}
}

func TestRendererPoolRoundRobin(t *testing.T) {
	r := &Renderer{cdpURLs: []string{"ws://a:9222", "ws://b:9222"}}
	if got := r.nextCDP(); got != "ws://a:9222" {
		t.Errorf("cdp #1 = %s", got)
	}
	if got := r.nextCDP(); got != "ws://b:9222" {
		t.Errorf("cdp #2 = %s", got)
	}
	if got := r.nextCDP(); got != "ws://a:9222" {
		t.Errorf("cdp #3 (wrap) = %s", got)
	}
}

func TestNewRendererFromEnvMulti(t *testing.T) {
	t.Setenv("CRAWLER_CDP_URL", "ws://x:9222, ws://y:9223 ,")
	r := NewRendererFromEnv()
	if len(r.cdpURLs) != 2 {
		t.Fatalf("cdpURLs = %v, mau 2 entri", r.cdpURLs)
	}
}

func TestScrapeRenderJSUnavailable(t *testing.T) {
	t.Setenv("CRAWLER_CDP_URL", "")
	_, err := Scrape(context.Background(), Options{
		URL:               "https://example.com",
		RenderJS:          true,
		SkipRobots:        true,
		AllowPrivateHosts: true,
	})
	if !errors.Is(err, ErrRenderUnavailable) {
		t.Fatalf("mau ErrRenderUnavailable, dapat: %v", err)
	}
}

// TestRenderLive menguji render nyata via Lightpanda/Chrome.
// Jalan hanya bila CRAWLER_CDP_URL di-set (mis. ws://127.0.0.1:9222).
func TestRenderLive(t *testing.T) {
	if os.Getenv("CRAWLER_CDP_URL") == "" {
		t.Skip("CRAWLER_CDP_URL tidak di-set; lewati test render live")
	}
	res, err := Scrape(context.Background(), Options{
		URL:        "https://demo-browser.lightpanda.io/campfire-commerce/",
		RenderJS:   true,
		SkipRobots: true,
	})
	if err != nil {
		t.Fatalf("Scrape render: %v", err)
	}
	if res.Markdown == "" {
		t.Error("markdown kosong dari halaman ter-render")
	}
}
