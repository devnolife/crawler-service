// Package ollama adalah klien minimal untuk Ollama /api/generate.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Client memanggil satu instance Ollama.
type Client struct {
	BaseURL string
	Model   string
	http    *http.Client
}

// NewFromEnv membaca OLLAMA_URL dan OLLAMA_MODEL (dengan default).
func NewFromEnv() *Client {
	baseURL := os.Getenv("OLLAMA_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11434"
	}
	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "qwen2.5:7b-instruct"
	}
	return &Client{
		BaseURL: baseURL,
		Model:   model,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

// Generate memanggil /api/generate non-streaming dan mengembalikan teks respons.
func (c *Client) Generate(ctx context.Context, prompt string, temperature float64, maxTokens int) (string, error) {
	return c.generate(ctx, prompt, temperature, maxTokens, nil)
}

// GenerateStructured seperti Generate tetapi memaksa output mengikuti JSON
// schema lewat field "format" Ollama (constrained decoding).
func (c *Client) GenerateStructured(ctx context.Context, prompt string, temperature float64, maxTokens int, schema json.RawMessage) (string, error) {
	return c.generate(ctx, prompt, temperature, maxTokens, schema)
}

func (c *Client) generate(ctx context.Context, prompt string, temperature float64, maxTokens int, format json.RawMessage) (string, error) {
	payload := map[string]any{
		"model":  c.Model,
		"prompt": prompt,
		"stream": false,
		"options": map[string]any{
			"temperature": temperature,
			"num_predict": maxTokens,
		},
	}
	if len(format) > 0 {
		payload["format"] = format
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama error: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama error: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("ollama error: %w", err)
	}
	return out.Response, nil
}
