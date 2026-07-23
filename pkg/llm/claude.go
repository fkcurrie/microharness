package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type ClaudeClient struct {
	apiKey string
	model  string
	http   *http.Client
}

func NewClaudeClient(apiKey, model string) *ClaudeClient {
	if model == "" {
		model = "claude-3-5-sonnet-latest"
	}
	return &ClaudeClient{
		apiKey: apiKey,
		model:  model,
		http:   &http.Client{Timeout: 60 * time.Second},
	}
}

type claudeMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeReq struct {
	Model     string      `json:"model"`
	MaxTokens int         `json:"max_tokens"`
	Messages  []claudeMsg `json:"messages"`
}

type claudeResp struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

func (c *ClaudeClient) Generate(ctx context.Context, prompt string, history []Message) (string, error) {
	var msgs []claudeMsg
	for _, h := range history {
		if h.Role == "user" || h.Role == "assistant" {
			msgs = append(msgs, claudeMsg{Role: h.Role, Content: h.Content})
		}
	}
	msgs = append(msgs, claudeMsg{Role: "user", Content: prompt})

	bodyData, err := json.Marshal(claudeReq{
		Model:     c.model,
		MaxTokens: 2048,
		Messages:  msgs,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(bodyData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("claude request error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("claude api error code: %d", resp.StatusCode)
	}

	var res claudeResp
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	if len(res.Content) > 0 {
		return res.Content[0].Text, nil
	}

	return "", fmt.Errorf("empty response returned from claude")
}
