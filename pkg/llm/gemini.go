package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type GeminiClient struct {
	apiKey string
	model  string
	http   *http.Client
}

func NewGeminiClient(apiKey, model string) *GeminiClient {
	if model == "" {
		model = "gemini-2.5-flash"
	}
	return &GeminiClient{
		apiKey: apiKey,
		model:  model,
		http:   &http.Client{Timeout: 60 * time.Second},
	}
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiReq struct {
	Contents []geminiContent `json:"contents"`
}

type geminiResp struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func (c *GeminiClient) Generate(ctx context.Context, prompt string, history []Message) (string, error) {
	var contents []geminiContent
	for _, h := range history {
		role := "user"
		if h.Role == "assistant" {
			role = "model"
		}
		contents = append(contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: h.Content}},
		})
	}
	if len(history) == 0 || history[len(history)-1].Content != prompt {
		contents = append(contents, geminiContent{
			Role:  "user",
			Parts: []geminiPart{{Text: prompt}},
		})
	}

	bodyData, err := json.Marshal(geminiReq{Contents: contents})
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", c.model, c.apiKey)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini request error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini api error code: %d", resp.StatusCode)
	}

	var res geminiResp
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	if len(res.Candidates) > 0 && len(res.Candidates[0].Content.Parts) > 0 {
		return res.Candidates[0].Content.Parts[0].Text, nil
	}

	return "", fmt.Errorf("no response candidate returned from gemini")
}
