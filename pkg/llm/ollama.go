package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type OllamaClient struct {
	endpoint string
	model    string
	http     *http.Client
}

func NewOllamaClient(endpoint, model string) *OllamaClient {
	if endpoint == "" {
		endpoint = "http://127.0.0.1:11434"
	}
	if model == "" {
		model = "gemma2:2b"
	}
	return &OllamaClient{
		endpoint: endpoint,
		model:    model,
		http:     &http.Client{Timeout: 60 * time.Second},
	}
}

type ollamaChatRequest struct {
	Model    string                 `json:"model"`
	Messages []ollamaMessage        `json:"messages"`
	Stream   bool                   `json:"stream"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
}

func (c *OllamaClient) Generate(ctx context.Context, prompt string, history []Message) (string, error) {
	var reqMsgs []ollamaMessage
	for _, h := range history {
		reqMsgs = append(reqMsgs, ollamaMessage{Role: h.Role, Content: h.Content})
	}
	if len(history) == 0 || history[len(history)-1].Content != prompt {
		reqMsgs = append(reqMsgs, ollamaMessage{Role: "user", Content: prompt})
	}

	bodyData, err := json.Marshal(ollamaChatRequest{
		Model:    c.model,
		Messages: reqMsgs,
		Stream:   false,
		Options: map[string]interface{}{
			"num_ctx":     2048,
			"num_predict": 60,
		},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/api/chat", bytes.NewBuffer(bodyData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama connection error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama api returned status %d", resp.StatusCode)
	}

	var res ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	return res.Message.Content, nil
}
