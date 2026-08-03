package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/devnolife/crawler-service/internal/scrape"
)

// --------------------------------------------------------- /api/v1/extract

// maxExtractMarkdown membatasi markdown yang dikirim ke LLM (~32k karakter)
// agar tidak melebihi context window model.
const maxExtractMarkdown = 32_000

type extractRequest struct {
	// URL untuk di-scrape lalu diekstrak. Salah satu URL/Markdown wajib.
	URL string `json:"url,omitempty"`
	// Markdown langsung (skip scrape) — misal hasil /scrape sebelumnya.
	Markdown string `json:"markdown,omitempty"`
	// Schema JSON schema untuk structured output (wajib, object).
	Schema json.RawMessage `json:"schema"`
	// Prompt instruksi tambahan (opsional).
	Prompt string `json:"prompt,omitempty"`
	// TimeoutMS batas scrape (1000–60000). Default 30000.
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

type extractResponse struct {
	URL       string          `json:"url,omitempty"`
	Title     string          `json:"title,omitempty"`
	Data      json.RawMessage `json:"data"`
	ScrapedAt *time.Time      `json:"scraped_at,omitempty"`
}

// handleExtract: scrape (opsional) → markdown → Ollama dengan JSON schema →
// structured JSON. Ala Firecrawl /extract, memakai constrained decoding
// Ollama (field "format") sehingga output dijamin valid terhadap schema.
func (s *Server) handleExtract(w http.ResponseWriter, r *http.Request) {
	var req extractRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "body JSON tidak valid: "+err.Error())
		return
	}
	if err := validateSchema(req.Schema); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if (req.URL == "") == (req.Markdown == "") {
		writeError(w, http.StatusBadRequest, "isi tepat salah satu: url atau markdown")
		return
	}

	resp := extractResponse{}
	markdown := req.Markdown

	if req.URL != "" {
		timeout := 30 * time.Second
		if req.TimeoutMS > 0 {
			if req.TimeoutMS < 1000 || req.TimeoutMS > 60000 {
				writeError(w, http.StatusBadRequest, "timeout_ms harus 1000–60000")
				return
			}
			timeout = time.Duration(req.TimeoutMS) * time.Millisecond
		}
		res, err := scrape.Scrape(r.Context(), scrape.Options{
			URL:             req.URL,
			OnlyMainContent: true,
			Timeout:         timeout,
		})
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, scrape.ErrBlockedByRobots) {
				status = http.StatusForbidden
			}
			writeError(w, status, err.Error())
			return
		}
		markdown = res.Markdown
		resp.URL = res.URL
		resp.Title = res.Title
		resp.ScrapedAt = &res.ScrapedAt
	}

	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		writeError(w, http.StatusUnprocessableEntity, "konten kosong — tidak ada yang bisa diekstrak")
		return
	}
	if len(markdown) > maxExtractMarkdown {
		markdown = markdown[:maxExtractMarkdown]
	}

	prompt := buildExtractPrompt(markdown, req.Prompt, req.Schema)
	out, err := s.llm.GenerateStructured(r.Context(), prompt, 0.1, 2048, req.Schema)
	if err != nil {
		writeError(w, http.StatusBadGateway, "llm error: "+err.Error())
		return
	}

	data := json.RawMessage(strings.TrimSpace(out))
	if !json.Valid(data) {
		writeError(w, http.StatusBadGateway, "llm mengembalikan JSON tidak valid")
		return
	}
	resp.Data = data
	writeJSON(w, http.StatusOK, resp)
}

// validateSchema memastikan schema adalah objek JSON schema minimal.
func validateSchema(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("field schema wajib diisi (JSON schema object)")
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("schema bukan objek JSON valid: %w", err)
	}
	if len(obj) == 0 {
		return errors.New("schema tidak boleh kosong")
	}
	return nil
}

func buildExtractPrompt(markdown, instruction string, schema json.RawMessage) string {
	var b strings.Builder
	b.WriteString("Kamu adalah mesin ekstraksi data. Ekstrak informasi dari konten " +
		"halaman web berikut sesuai JSON schema yang diberikan. " +
		"Jawab HANYA dengan JSON valid, tanpa penjelasan. " +
		"Gunakan null/string kosong untuk field yang tidak ditemukan — jangan mengarang.\n\n")
	if instruction != "" {
		b.WriteString("Instruksi tambahan: " + instruction + "\n\n")
	}
	b.WriteString("JSON schema:\n")
	b.Write(schema)
	b.WriteString("\n\nKonten halaman (markdown):\n\"\"\"\n")
	b.WriteString(markdown)
	b.WriteString("\n\"\"\"\n")
	return b.String()
}
