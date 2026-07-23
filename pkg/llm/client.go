package llm

import (
	"context"
	"fmt"
	"time"

	"microharness/pkg/config"
)

type Message struct {
	Role    string `json:"role"`    // "user", "assistant", "system"
	Content string `json:"content"`
}

type Client interface {
	Generate(ctx context.Context, prompt string, history []Message) (string, error)
}

type RetryingClient struct {
	inner      Client
	maxRetries int
}

func NewRetryingClient(inner Client, maxRetries int) Client {
	if maxRetries <= 0 {
		maxRetries = 3
	}
	return &RetryingClient{
		inner:      inner,
		maxRetries: maxRetries,
	}
}

func (r *RetryingClient) Generate(ctx context.Context, prompt string, history []Message) (string, error) {
	var lastErr error
	backoff := 500 * time.Millisecond

	for attempt := 1; attempt <= r.maxRetries; attempt++ {
		resp, err := r.inner.Generate(ctx, prompt, history)
		if err == nil {
			return resp, nil
		}

		lastErr = err

		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		if attempt < r.maxRetries {
			select {
			case <-time.After(backoff):
				backoff *= 2 // Exponential backoff: 500ms, 1000ms
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}

	return "", fmt.Errorf("retried %d times, last error: %w", r.maxRetries, lastErr)
}

func NewClient(cfg *config.LLMConfig) (Client, error) {
	var baseClient Client
	switch cfg.DefaultProvider {
	case "ollama":
		baseClient = NewOllamaClient(cfg.Ollama.Endpoint, cfg.Ollama.Model)
	case "gemini":
		if cfg.Gemini.APIKey == "" {
			if cfg.Ollama.Model != "" {
				baseClient = NewOllamaClient(cfg.Ollama.Endpoint, cfg.Ollama.Model)
			} else {
				return nil, fmt.Errorf("gemini provider selected but GEMINI_API_KEY is empty")
			}
		} else {
			baseClient = NewGeminiClient(cfg.Gemini.APIKey, cfg.Gemini.Model)
		}
	case "claude":
		if cfg.Claude.APIKey == "" {
			if cfg.Ollama.Model != "" {
				baseClient = NewOllamaClient(cfg.Ollama.Endpoint, cfg.Ollama.Model)
			} else {
				return nil, fmt.Errorf("claude provider selected but ANTHROPIC_API_KEY is empty")
			}
		} else {
			baseClient = NewClaudeClient(cfg.Claude.APIKey, cfg.Claude.Model)
		}
	case "litellm":
		baseClient = NewLiteLLMClient(cfg.LiteLLM.Endpoint, cfg.LiteLLM.Model, cfg.LiteLLM.APIKey)
	default:
		baseClient = NewOllamaClient(cfg.Ollama.Endpoint, cfg.Ollama.Model)
	}

	return NewRetryingClient(baseClient, 3), nil
}
