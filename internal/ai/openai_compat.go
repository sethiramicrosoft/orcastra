package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAICompatProvider works with any OpenAI-compatible endpoint:
// OpenAI, OpenRouter, Groq, Together AI, Mistral, LM Studio, Ollama (/v1).
type OpenAICompatProvider struct {
	baseURL string
	apiKey  string
	model   string
	name    string
	client  *http.Client
}

func NewOpenAICompatProvider(cfg Config, displayName string) *OpenAICompatProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	name := displayName
	if name == "" {
		name = "openai_compat"
	}
	return &OpenAICompatProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		name:    name,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (p *OpenAICompatProvider) Name() string  { return p.name }
func (p *OpenAICompatProvider) Model() string { return p.model }

func (p *OpenAICompatProvider) Analyze(ctx context.Context, req AnalysisRequest) (*AnalysisResult, error) {
	prompt := buildPrompt(req)

	body, _ := json.Marshal(map[string]any{
		"model": p.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": prompt},
		},
		"max_tokens":  400,
		"temperature": 0.2,
	})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	// OpenRouter requires this header to identify the app
	httpReq.Header.Set("X-Title", "Orcastra")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider returned %d: %s", resp.StatusCode, string(b))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("provider returned no choices")
	}

	return parseAnalysisResponse(out.Choices[0].Message.Content, p.model, p.name), nil
}
