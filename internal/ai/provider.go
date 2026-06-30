// Package ai defines the AIProvider interface and all provider implementations.
// Users bring their own API key for any supported provider.
// OpenAI-compatible base URL covers: OpenAI, OpenRouter, Groq, Together AI,
// Mistral, LM Studio, Ollama (no key), and any other OpenAI-compatible endpoint.
package ai

import "context"

// AnalysisRequest is the input for deploy failure analysis.
type AnalysisRequest struct {
	// Last N lines of the deploy log (stdout + stderr combined)
	LogLines []string
	// Service name, image, trigger type — for context
	ServiceName string
	TriggerType string
	CommitSHA   string
}

// AnalysisResult is the structured output of deploy failure analysis.
type AnalysisResult struct {
	// One-sentence plain-English diagnosis
	Diagnosis string
	// Concrete suggested fix (code snippet or command, if applicable)
	Suggestion string
	// Confidence: "high", "medium", "low" — shown to user
	Confidence string
	// Model that produced this (for transparency)
	Model string
	// Provider that produced this
	Provider string
}

// Provider is the single interface all AI backends implement.
// Add new providers by implementing this interface — nothing else changes.
type Provider interface {
	// Analyze reads deploy logs and returns a failure diagnosis.
	Analyze(ctx context.Context, req AnalysisRequest) (*AnalysisResult, error)
	// Name returns the provider name for display (e.g. "openrouter", "gemini").
	Name() string
	// Model returns the model being used (e.g. "gpt-4o", "claude-3-5-sonnet").
	Model() string
}

// ProviderType is the stored discriminator in the database.
type ProviderType string

const (
	ProviderOpenAICompat ProviderType = "openai_compat" // OpenAI, OpenRouter, Groq, Together, Mistral, Ollama, LM Studio
	ProviderAnthropic    ProviderType = "anthropic"
	ProviderGemini       ProviderType = "gemini"
	ProviderOllama       ProviderType = "ollama" // local, no key
)

// Config is the user-supplied provider configuration stored (encrypted) per team.
type Config struct {
	Type    ProviderType `json:"type"`
	// BaseURL overrides the default endpoint.
	// OpenRouter: https://openrouter.ai/api/v1
	// Groq:       https://api.groq.com/openai/v1
	// Ollama:     http://localhost:11434/v1
	// OpenAI:     https://api.openai.com/v1 (default, can omit)
	BaseURL string `json:"base_url,omitempty"`
	// APIKey — empty for Ollama (local)
	APIKey  string `json:"api_key,omitempty"`
	// Model to use. Examples:
	// OpenAI:     gpt-4o, gpt-4o-mini
	// OpenRouter: anthropic/claude-3-5-sonnet, google/gemini-pro, meta-llama/llama-3-70b
	// Groq:       llama3-70b-8192, mixtral-8x7b-32768
	// Anthropic:  claude-3-5-sonnet-20241022
	// Gemini:     gemini-1.5-pro, gemini-1.5-flash
	// Ollama:     llama3, mistral, phi3
	Model   string `json:"model"`
}

// KnownPresets are the well-known base URLs shown in the UI picker.
// Users can also type a custom URL.
var KnownPresets = []Preset{
	{Name: "OpenAI",      Type: ProviderOpenAICompat, BaseURL: "https://api.openai.com/v1",          DefaultModel: "gpt-4o-mini"},
	{Name: "OpenRouter",  Type: ProviderOpenAICompat, BaseURL: "https://openrouter.ai/api/v1",       DefaultModel: "anthropic/claude-3-haiku"},
	{Name: "Groq",        Type: ProviderOpenAICompat, BaseURL: "https://api.groq.com/openai/v1",     DefaultModel: "llama3-70b-8192"},
	{Name: "Together AI", Type: ProviderOpenAICompat, BaseURL: "https://api.together.xyz/v1",        DefaultModel: "meta-llama/Llama-3-70b-chat-hf"},
	{Name: "Mistral",     Type: ProviderOpenAICompat, BaseURL: "https://api.mistral.ai/v1",          DefaultModel: "mistral-large-latest"},
	{Name: "Anthropic",   Type: ProviderAnthropic,    BaseURL: "",                                   DefaultModel: "claude-3-5-sonnet-20241022"},
	{Name: "Gemini",      Type: ProviderGemini,       BaseURL: "",                                   DefaultModel: "gemini-1.5-flash"},
	{Name: "Ollama",      Type: ProviderOllama,       BaseURL: "http://localhost:11434/v1",           DefaultModel: "llama3"},
	{Name: "Custom",      Type: ProviderOpenAICompat, BaseURL: "",                                   DefaultModel: ""},
}

// Preset is one entry in the UI provider picker.
type Preset struct {
	Name         string
	Type         ProviderType
	BaseURL      string
	DefaultModel string
}
