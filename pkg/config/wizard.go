package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type OllamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// AutoDiscover attempts to probe the environment for existing services and credentials
func AutoDiscover() (*Config, map[string]string) {
	cfg := DefaultConfig()
	discovered := make(map[string]string)

	// 1. Probe Gemini API Key
	if key := os.Getenv("GEMINI_API_KEY"); key != "" {
		cfg.LLM.Gemini.APIKey = key
		discovered["Gemini API Key"] = "Detected from $GEMINI_API_KEY"
	}

	// 2. Probe Claude / Anthropic API Key
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		cfg.LLM.Claude.APIKey = key
		discovered["Claude API Key"] = "Detected from $ANTHROPIC_API_KEY"
	} else if key := os.Getenv("CLAUDE_API_KEY"); key != "" {
		cfg.LLM.Claude.APIKey = key
		discovered["Claude API Key"] = "Detected from $CLAUDE_API_KEY"
	}

	// 3. Probe Local Ollama Instance
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:11434/api/tags")
	if err == nil && resp.StatusCode == 200 {
		var tags OllamaTagsResponse
		if err := json.NewDecoder(resp.Body).Decode(&tags); err == nil && len(tags.Models) > 0 {
			cfg.LLM.Ollama.Endpoint = "http://127.0.0.1:11434"
			cfg.LLM.Ollama.Model = tags.Models[0].Name
			discovered["Local Ollama Server"] = fmt.Sprintf("Found at 127.0.0.1:11434 with models: %s", tags.Models[0].Name)
		}
		resp.Body.Close()
	}

	// 4. Auto-detect SSH Key in ~/.ssh
	home, _ := os.UserHomeDir()
	sshDir := filepath.Join(home, ".ssh")
	for _, keyName := range []string{"id_ed25519", "id_rsa"} {
		keyPath := filepath.Join(sshDir, keyName)
		if _, err := os.Stat(keyPath); err == nil {
			discovered["Default SSH Key"] = keyPath
			break
		}
	}

	return cfg, discovered
}
