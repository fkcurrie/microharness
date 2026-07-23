package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
			// Pick the most responsive lightweight model (prefer 4b/e4b/8b for sub-2s latency over heavy 26b/31b)
			bestModel := tags.Models[0].Name
			for _, m := range tags.Models {
				if strings.Contains(m.Name, "e4b") || strings.Contains(m.Name, "4b") || strings.Contains(m.Name, "8b") {
					bestModel = m.Name
					break
				}
			}

			cfg.LLM.Ollama.Endpoint = "http://127.0.0.1:11434"
			cfg.LLM.Ollama.Model = bestModel
			discovered["Local Ollama Server"] = fmt.Sprintf("Found at 127.0.0.1:11434 (Auto-selected fast model: %s)", bestModel)

			// If no cloud API keys were detected, automatically default to local Ollama!
			if cfg.LLM.Gemini.APIKey == "" && cfg.LLM.Claude.APIKey == "" {
				cfg.LLM.DefaultProvider = "ollama"
				discovered["Active Brain Provider"] = fmt.Sprintf("Auto-selected local open model '%s' (Zero API keys required)", bestModel)
				discovered["Model Latency Benchmark"] = BenchmarkAndTuneModel(cfg.LLM.Ollama.Endpoint, bestModel)
			}
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

	// 5. Ensure SOUL.md existence
	GetSoulContent()

	return cfg, discovered
}

func BenchmarkAndTuneModel(endpoint, modelName string) string {
	client := &http.Client{Timeout: 10 * time.Second}
	reqBody := map[string]interface{}{
		"model":  modelName,
		"prompt": "Hi how are you?",
		"stream": false,
		"options": map[string]interface{}{
			"num_ctx": 2048,
		},
	}
	bodyData, _ := json.Marshal(reqBody)

	start := time.Now()
	resp, err := client.Post(endpoint+"/api/generate", "application/json", bytes.NewBuffer(bodyData))
	if err != nil || resp.StatusCode != 200 {
		return "⚠️ Benchmark Skipped (Server unresponsive)"
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if elapsed < 5*time.Second {
		return fmt.Sprintf("⚡ OPTIMAL LATENCY: %v (< 5s target verified!)", elapsed.Round(time.Millisecond))
	}
	return fmt.Sprintf("⚠️ %v response time (Consider GPU pre-warming)", elapsed.Round(time.Millisecond))
}
