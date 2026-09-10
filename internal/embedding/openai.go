package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	Model             = "text-embedding-3-small"
	Dimensions        = 1536
	defaultEndpoint   = "https://api.openai.com/v1/embeddings"
	maxInputsPerBatch = 2048
	maxErrorBodyBytes = 4096
)

var ErrInvalidAPIKey = errors.New("OpenAI API key must not be empty")

type ProviderError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("OpenAI returned %s: %s", e.Status, e.Body)
}

func (e *ProviderError) Retryable() bool {
	switch e.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}

	return e.StatusCode >= http.StatusInternalServerError
}

// OpenAIClient generates embeddings through the OpenAI embeddings API.
type OpenAIClient struct {
	apiKey     string
	httpClient *http.Client
	endpoint   string
}

// NewOpenAIClient creates an embedding client with a bounded request timeout.
func NewOpenAIClient(apiKey string) (*OpenAIClient, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, ErrInvalidAPIKey
	}

	return &OpenAIClient{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		endpoint: defaultEndpoint,
	}, nil
}

// Embed returns one embedding per input in the same order as the inputs.
func (c *OpenAIClient) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	for index, input := range inputs {
		if strings.TrimSpace(input) == "" {
			return nil, fmt.Errorf("input %d must not be empty", index)
		}
	}

	embeddings := make([][]float32, 0, len(inputs))
	for start := 0; start < len(inputs); start += maxInputsPerBatch {
		end := min(start+maxInputsPerBatch, len(inputs))
		batch, err := c.embedBatch(ctx, inputs[start:end])
		if err != nil {
			return nil, fmt.Errorf("embed input batch beginning at %d: %w", start, err)
		}
		embeddings = append(embeddings, batch...)
	}

	return embeddings, nil
}

type embeddingsRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
	Dimensions     int      `json:"dimensions"`
}

type embeddingsResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func (c *OpenAIClient) embedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(embeddingsRequest{
		Model:          Model,
		Input:          inputs,
		EncodingFormat: "float",
		Dimensions:     Dimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		providerMessage, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		return nil, &ProviderError{
			StatusCode: response.StatusCode,
			Status:     response.Status,
			Body:       strings.TrimSpace(string(providerMessage)),
		}
	}

	var result embeddingsResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Data) != len(inputs) {
		return nil, fmt.Errorf("OpenAI returned %d embeddings for %d inputs", len(result.Data), len(inputs))
	}

	embeddings := make([][]float32, len(inputs))
	for _, item := range result.Data {
		if item.Index < 0 || item.Index >= len(inputs) {
			return nil, fmt.Errorf("OpenAI returned out-of-range embedding index %d", item.Index)
		}
		if embeddings[item.Index] != nil {
			return nil, fmt.Errorf("OpenAI returned duplicate embedding index %d", item.Index)
		}
		if len(item.Embedding) != Dimensions {
			return nil, fmt.Errorf("OpenAI returned embedding dimension %d, want %d", len(item.Embedding), Dimensions)
		}
		embeddings[item.Index] = item.Embedding
	}

	return embeddings, nil
}
