package tui

import (
	"strings"
	"testing"

	"microharness/pkg/config"
)

func TestTUIRenderIncludesGPU(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{
			DefaultProvider: "ollama",
			Ollama:          config.OllamaConfig{Model: "gemma3:4b"},
		},
	}

	m := NewModel(cfg, nil, nil, nil)
	m.selectingSess = false
	m.width = 80
	m.height = 24

	rendered := m.View()
	if !strings.Contains(rendered, "GPU:") {
		t.Fatalf("Expected rendered TUI View to contain 'GPU:', got:\n%s", rendered)
	}
}
