package ai

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
)

const lmStudioTimeout = 90 * time.Second

// Client handles communication with the LM Studio server using the OpenAI SDK.
type Client struct {
	mu        sync.RWMutex
	apiClient *openai.Client
	model     string
}

// NewClient creates a new AI client using the provided base URL and model name.
func NewClient(baseURL string, model string) *Client {
	// LM Studio uses a dummy API key, but the SDK requires one to be present.
	config := openai.DefaultConfig(baseURL)
	config.BaseURL = baseURL
	config.HTTPClient = &http.Client{Timeout: lmStudioTimeout}

	return &Client{
		apiClient: openai.NewClientWithConfig(config),
		model:     model,
	}
}

// UpdateConfig replaces the LM Studio endpoint and model (e.g. after live config reload).
func (c *Client) UpdateConfig(baseURL, model string) {
	conf := openai.DefaultConfig(baseURL)
	conf.BaseURL = baseURL
	conf.HTTPClient = &http.Client{Timeout: lmStudioTimeout}
	c.mu.Lock()
	c.apiClient = openai.NewClientWithConfig(conf)
	c.model = model
	c.mu.Unlock()
}

// Ask sends a question to the LM Studio server and returns the response content.
func (c *Client) Ask(ctx context.Context, question string) (string, error) {
	c.mu.RLock()
	client := c.apiClient
	model := c.model
	c.mu.RUnlock()
	// We use a system message to define the persona and ensure speed/conciseness.
	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleSystem,
			Content: "You are a highly concise IRC bot. Rules:\n1. Provide the shortest possible accurate answer.\n2. No reasoning, no 'thinking', no preamble, and no conversational filler (e.g., do not say 'Sure!' or 'Here is your answer').\n3. Use plain text only. No markdown, no bolding, no italics, no lists, and no special characters.\n4. If a single word suffices, use only one word.",
		},
		{
			Role:    openai.ChatMessageRoleUser,
			Content: question,
		},
	}

	// We use the model name provided in the configuration.
	resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    model,
		Messages: messages,
	})

	if err != nil {
		return "", fmt.Errorf("failed to request completion from LM Studio: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices returned from LM Studio")
	}

	answer, ok := stripThink(resp.Choices[0].Message.Content)
	if !ok {
		return "", fmt.Errorf("model %s returned only reasoning (<think> never closed); try a non-thinking model", model)
	}
	return answer, nil
}

// stripThink drops the reasoning block that thinking models (Qwen3, DeepSeek-R1, ...) put before
// the answer, so any model can be picked without a per-model setting. Some chat templates open
// <think> in the prompt, so everything up to the last </think> goes. ok is false when the reply
// is an unterminated <think> (the model ran out of tokens while still reasoning).
func stripThink(s string) (answer string, ok bool) {
	if i := strings.LastIndex(s, "</think>"); i >= 0 {
		return strings.TrimSpace(s[i+len("</think>"):]), true
	}
	if strings.HasPrefix(strings.TrimSpace(s), "<think>") {
		return "", false
	}
	return s, true
}

// NewModelLister returns a client for baseURL used only to list its models (dashboard picker).
func NewModelLister(baseURL string) *Client { return NewClient(baseURL, "") }

// ListModels returns the chat model ids the server offers (GET /models), sorted, without
// embedding models (they can't answer !ask).
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	c.mu.RLock()
	client := c.apiClient
	c.mu.RUnlock()
	list, err := client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list models: %w", err)
	}
	ids := make([]string, 0, len(list.Models))
	for _, m := range list.Models {
		if !strings.Contains(strings.ToLower(m.ID), "embed") {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
