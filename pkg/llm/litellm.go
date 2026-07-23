package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type LiteLLMClient struct {
	endpoint string
	model    string
	apiKey   string
	http     *http.Client
}

func NewLiteLLMClient(endpoint, model, apiKey string) *LiteLLMClient {
	if endpoint == "" {
		endpoint = "http://127.0.0.1:4000"
	}
	if model == "" {
		model = "gemini-3.5-flash"
	}
	return &LiteLLMClient{
		endpoint: endpoint,
		model:    model,
		apiKey:   apiKey,
		http:     &http.Client{Timeout: 60 * time.Second},
	}
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatReq struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
}

type openAIChatResp struct {
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
}

func (c *LiteLLMClient) Generate(ctx context.Context, prompt string, history []Message) (string, error) {
	var reqMsgs []openAIMessage
	for _, h := range history {
		reqMsgs = append(reqMsgs, openAIMessage{Role: h.Role, Content: h.Content})
	}
	reqMsgs = append(reqMsgs, openAIMessage{Role: "user", Content: prompt})

	bodyData, err := json.Marshal(openAIChatReq{
		Model:    c.model,
		Messages: reqMsgs,
	})
	if err != nil {
		return "", err
	}

	url := c.endpoint + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("litellm connection error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("litellm proxy returned status %d", resp.StatusCode)
	}

	var res openAIChatResp
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	if len(res.Choices) > 0 {
		return res.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("empty choice returned from litellm proxy")
}
