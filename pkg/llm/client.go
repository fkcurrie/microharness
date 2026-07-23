package llm

import (
	"context"
	"fmt"
	"microharness/pkg/config"
)

type Message struct {
	Role    string `json:"role"`    // "user", "assistant", "system"
	Content string `json:"content"`
}

type Client interface {
	Generate(ctx context.Context, prompt string, history []Message) (string, error)
}

func NewClient(cfg *config.LLMConfig) (Client, error) {
	switch cfg.DefaultProvider {
	case "ollama":
		return NewOllamaClient(cfg.Ollama.Endpoint, cfg.Ollama.Model), nil
	case "gemini":
		if cfg.Gemini.APIKey == "" {
			return nil, fmt.Errorf("gemini provider selected but GEMINI_API_KEY is empty")
		}
		return NewGeminiClient(cfg.Gemini.APIKey, cfg.Gemini.Model), nil
	case "claude":
		if cfg.Claude.APIKey == "" {
			return nil, fmt.Errorf("claude provider selected but ANTHROPIC_API_KEY is empty")
		}
		return NewClaudeClient(cfg.Claude.APIKey, cfg.Claude.Model), nil
	default:
		// Fallback to Ollama or return error
		return NewOllamaClient(cfg.Ollama.Endpoint, cfg.Ollama.Model), nil
	}
}
