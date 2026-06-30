package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

const systemPrompt = `You are a deployment failure analyst for Orcastra, a self-hosted platform.
A user's deployment has failed. You will be shown the last lines of the deploy log.
Your job:
1. Identify the root cause in ONE plain-English sentence (no jargon unless necessary).
2. Give ONE concrete suggested fix — a command to run, a config change, or a file to check.
3. Rate your confidence: high / medium / low.

Respond ONLY in this JSON format, nothing else:
{
  "diagnosis": "...",
  "suggestion": "...",
  "confidence": "high|medium|low"
}`

// buildPrompt constructs the user message from the analysis request.
func buildPrompt(req AnalysisRequest) string {
	logText := strings.Join(req.LogLines, "\n")
	return fmt.Sprintf(`Service: %s
Trigger: %s
Commit: %s

Last deploy log lines:
---
%s
---

Analyze the failure above.`, req.ServiceName, req.TriggerType, req.CommitSHA, logText)
}

// parseAnalysisResponse parses the structured JSON the LLM is prompted to return.
// Falls back gracefully if the model doesn't follow instructions exactly.
func parseAnalysisResponse(content, model, provider string) *AnalysisResult {
	// Try strict JSON parse first
	var parsed struct {
		Diagnosis  string `json:"diagnosis"`
		Suggestion string `json:"suggestion"`
		Confidence string `json:"confidence"`
	}

	// Strip markdown code fences if the model added them
	clean := strings.TrimSpace(content)
	if idx := strings.Index(clean, "{"); idx > 0 {
		clean = clean[idx:]
	}
	if idx := strings.LastIndex(clean, "}"); idx >= 0 {
		clean = clean[:idx+1]
	}

	if err := json.Unmarshal([]byte(clean), &parsed); err == nil && parsed.Diagnosis != "" {
		return &AnalysisResult{
			Diagnosis:  parsed.Diagnosis,
			Suggestion: parsed.Suggestion,
			Confidence: parsed.Confidence,
			Model:      model,
			Provider:   provider,
		}
	}

	// Fallback: return raw content as diagnosis
	return &AnalysisResult{
		Diagnosis:  strings.TrimSpace(content),
		Suggestion: "",
		Confidence: "low",
		Model:      model,
		Provider:   provider,
	}
}

// NewProvider constructs the correct Provider implementation from a Config.
func NewProvider(cfg Config) (Provider, error) {
	switch cfg.Type {
	case ProviderOpenAICompat:
		// Determine display name from base URL
		name := resolveDisplayName(cfg.BaseURL)
		return NewOpenAICompatProvider(cfg, name), nil
	case ProviderAnthropic:
		return NewAnthropicProvider(cfg), nil
	case ProviderGemini:
		return NewGeminiProvider(cfg), nil
	case ProviderOllama:
		// Ollama uses OpenAI-compat API, no key required
		ollamaCfg := cfg
		if ollamaCfg.BaseURL == "" {
			ollamaCfg.BaseURL = "http://localhost:11434/v1"
		}
		return NewOpenAICompatProvider(ollamaCfg, "ollama"), nil
	default:
		return nil, fmt.Errorf("unknown provider type: %s", cfg.Type)
	}
}

func resolveDisplayName(baseURL string) string {
	for _, p := range KnownPresets {
		if p.BaseURL != "" && strings.HasPrefix(baseURL, p.BaseURL) {
			return strings.ToLower(strings.ReplaceAll(p.Name, " ", "_"))
		}
	}
	return "openai_compat"
}
