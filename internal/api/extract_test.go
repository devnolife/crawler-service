package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devnolife/crawler-service/internal/ollama"
)

// newFakeOllama meniru POST /api/generate dan merekam payload terakhir.
func newFakeOllama(t *testing.T, response string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&last); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"response": response})
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

func newExtractServer(t *testing.T, llmResponse string) (*Server, *map[string]any) {
	t.Helper()
	fake, last := newFakeOllama(t, llmResponse)
	t.Setenv("OLLAMA_URL", fake.URL)
	t.Setenv("OLLAMA_MODEL", "model-uji")
	s := New(nil, nil)
	s.llm = ollama.NewFromEnv()
	return s, last
}

func postExtract(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/extract", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.handleExtract(rr, req)
	return rr
}

func TestExtractFromMarkdown(t *testing.T) {
	s, last := newExtractServer(t, `{"judul":"Skripsi ML","tahun":2024}`)

	rr := postExtract(t, s, `{
		"markdown": "# Skripsi ML\nDitulis tahun 2024 oleh Budi.",
		"schema": {"type":"object","properties":{"judul":{"type":"string"},"tahun":{"type":"integer"}}},
		"prompt": "ambil judul dan tahun"
	}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data["judul"] != "Skripsi ML" {
		t.Errorf("data.judul = %v", resp.Data["judul"])
	}

	// Schema harus diteruskan ke Ollama sebagai field "format".
	if (*last)["format"] == nil {
		t.Error("payload Ollama tidak memuat field format (JSON schema)")
	}
	if (*last)["model"] != "model-uji" {
		t.Errorf("model = %v", (*last)["model"])
	}
}

func TestExtractValidation(t *testing.T) {
	s, _ := newExtractServer(t, `{}`)

	cases := []struct {
		name, body string
		wantStatus int
	}{
		{"tanpa schema", `{"markdown":"abc"}`, http.StatusBadRequest},
		{"schema kosong", `{"markdown":"abc","schema":{}}`, http.StatusBadRequest},
		{"url dan markdown dua-duanya", `{"url":"https://x.id","markdown":"abc","schema":{"type":"object"}}`, http.StatusBadRequest},
		{"tanpa url dan markdown", `{"schema":{"type":"object"}}`, http.StatusBadRequest},
		{"markdown whitespace", `{"markdown":"   ","schema":{"type":"object"}}`, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := postExtract(t, s, tc.body)
			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body = %s", rr.Code, tc.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestExtractLLMInvalidJSON(t *testing.T) {
	s, _ := newExtractServer(t, `bukan json {{`)
	rr := postExtract(t, s, `{"markdown":"abc","schema":{"type":"object"}}`)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502; body = %s", rr.Code, rr.Body.String())
	}
}
