package ai

import (
	"context"
	"fmt"
	"net/http"

	"github.com/sashabaranov/go-openai"
)

// Client handles communication with the LM Studio server using the OpenAI SDK.
type Client struct {
	apiClient *openai.Client
	model     string
}

// NewClient creates a new AI client using the provided base URL and model name.
func NewClient(baseURL string, model string) *Client {
	// LM Studio uses a dummy API key, but the SDK requires one to be present.
	config := openai.DefaultConfig(baseURL)
	config.BaseURL = baseURL
	config.HTTPClient = &http.Client{}

	return &Client{
		apiClient: openai.NewClientWithConfig(config),
		model:     model,
	}
}

// Ask sends a question to the LM Studio server and returns the response content.
func (c *Client) Ask(ctx context.Context, question string) (string, error) {
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
	resp, err := c.apiClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    c.model,
		Messages: messages,
	})

	if err != nil {
		return "", fmt.Errorf("failed to request completion from LM Studio: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices returned from LM Studio")
	}

	return resp.Choices[0].Message.Content, nil
}
