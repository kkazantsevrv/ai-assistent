// Package llm — клиент DeepSeek API (OpenAI-совместимый формат chat/completions)
// с поддержкой вызова функций (tools).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ReasoningContent возвращает deepseek-reasoner; в рамках одного хода с
	// вызовами функций его нужно отправлять обратно, в историю не сохраняем.
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type ResponseFormat struct {
	Type string `json:"type"`
}

type Request struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Tools          []Tool          `json:"tools,omitempty"`
	ToolChoice     string          `json:"tool_choice,omitempty"`
	Temperature    *float64        `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	Stream         bool            `json:"stream"`
}

type response struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// APIError — ошибка от DeepSeek.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	switch e.Status {
	case 401:
		return "DeepSeek: неверный API-ключ (401)"
	case 402:
		return "DeepSeek: недостаточно средств на балансе (402)"
	case 429:
		return "DeepSeek: слишком много запросов (429), попробуйте позже"
	}
	return fmt.Sprintf("DeepSeek: HTTP %d: %s", e.Status, e.Message)
}

type Client struct {
	apiKey  string
	baseURL string
	Model   string
	http    *http.Client
}

func New(apiKey, baseURL, model string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		Model:   model,
		http:    &http.Client{Timeout: 5 * time.Minute},
	}
}

// Float — помощник для указателя на температуру.
func Float(f float64) *float64 { return &f }

// Chat отправляет запрос и возвращает ответ модели. Временные ошибки
// (сеть, 429, 5xx) повторяются несколько раз.
func (c *Client) Chat(ctx context.Context, req Request) (*Message, string, error) {
	if req.Model == "" {
		req.Model = c.Model
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, "", err
	}

	backoff := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}
	var lastErr error
	for attempt := 0; attempt <= len(backoff); attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(backoff[attempt-1]):
			case <-ctx.Done():
				return nil, "", ctx.Err()
			}
		}
		msg, finish, err := c.once(ctx, body)
		if err == nil {
			return msg, finish, nil
		}
		lastErr = err
		if ctx.Err() != nil || !retryable(err) {
			break
		}
	}
	return nil, "", lastErr
}

func retryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == 429 || ae.Status >= 500
	}
	return true // сетевые ошибки
}

func (c *Client) once(ctx context.Context, body []byte) (*Message, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, "", err
	}

	var r response
	jsonErr := json.Unmarshal(data, &r)
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("%.300s", data)
		if jsonErr == nil && r.Error != nil {
			msg = r.Error.Message
		}
		return nil, "", &APIError{Status: resp.StatusCode, Message: msg}
	}
	if jsonErr != nil {
		return nil, "", fmt.Errorf("DeepSeek: не удалось разобрать ответ: %w", jsonErr)
	}
	if len(r.Choices) == 0 {
		return nil, "", errors.New("DeepSeek: пустой ответ")
	}
	ch := r.Choices[0]
	return &ch.Message, ch.FinishReason, nil
}
