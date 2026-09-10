package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestNewOpenAIClientRejectsEmptyAPIKey(t *testing.T) {
	_, err := NewOpenAIClient("   ")
	if !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidAPIKey)
	}
}

func TestOpenAIClientEmbed(t *testing.T) {
	firstEmbedding := testEmbedding(1)
	secondEmbedding := testEmbedding(2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}

		var request embeddingsRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if request.Model != Model {
			t.Errorf("model = %q, want %q", request.Model, Model)
		}
		if want := []string{"first chunk", "second chunk"}; !slices.Equal(request.Input, want) {
			t.Errorf("inputs = %#v, want %#v", request.Input, want)
		}
		if request.EncodingFormat != "float" {
			t.Errorf("encoding format = %q, want float", request.EncodingFormat)
		}
		if request.Dimensions != Dimensions {
			t.Errorf("dimensions = %d, want %d", request.Dimensions, Dimensions)
		}

		writeTestResponse(w, []testResponseItem{
			{Index: 1, Embedding: secondEmbedding},
			{Index: 0, Embedding: firstEmbedding},
		})
	}))
	defer server.Close()

	client, err := NewOpenAIClient("test-api-key")
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.endpoint = server.URL

	got, err := client.Embed(context.Background(), []string{"first chunk", "second chunk"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("embedding count = %d, want 2", len(got))
	}
	if !slices.Equal(got[0], firstEmbedding) {
		t.Error("first embedding was not restored to input order")
	}
	if !slices.Equal(got[1], secondEmbedding) {
		t.Error("second embedding was not restored to input order")
	}
}

func TestOpenAIClientEmbedRejectsInvalidInput(t *testing.T) {
	client, err := NewOpenAIClient("test-api-key")
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	_, err = client.Embed(context.Background(), []string{"valid", "  "})
	if err == nil || !strings.Contains(err.Error(), "input 1 must not be empty") {
		t.Fatalf("error = %v, want empty input error", err)
	}
}

func TestOpenAIClientEmbedHandlesProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
	}))
	defer server.Close()

	client, err := NewOpenAIClient("test-api-key")
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.endpoint = server.URL

	_, err = client.Embed(context.Background(), []string{"chunk"})
	if err == nil || !strings.Contains(err.Error(), "429 Too Many Requests") {
		t.Fatalf("error = %v, want provider status error", err)
	}
}

func TestOpenAIClientEmbedRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name  string
		items []testResponseItem
		want  string
	}{
		{
			name:  "missing embedding",
			items: nil,
			want:  "0 embeddings for 1 inputs",
		},
		{
			name: "out-of-range index",
			items: []testResponseItem{
				{Index: 1, Embedding: testEmbedding(1)},
			},
			want: "out-of-range embedding index 1",
		},
		{
			name: "incorrect dimensions",
			items: []testResponseItem{
				{Index: 0, Embedding: []float32{1, 2}},
			},
			want: "embedding dimension 2, want 1536",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeTestResponse(w, tt.items)
			}))
			defer server.Close()

			client, err := NewOpenAIClient("test-api-key")
			if err != nil {
				t.Fatalf("create client: %v", err)
			}
			client.endpoint = server.URL

			_, err = client.Embed(context.Background(), []string{"chunk"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

type testResponseItem struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

func testEmbedding(firstValue float32) []float32 {
	embedding := make([]float32, Dimensions)
	embedding[0] = firstValue
	return embedding
}

func writeTestResponse(w http.ResponseWriter, items []testResponseItem) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
}

func TestProviderErrorClassifiesRetryableStatusCodes(t *testing.T) {
	tests := []struct {
		status    int
		retryable bool
	}{
		{status: http.StatusTooManyRequests, retryable: true},
		{status: http.StatusRequestTimeout, retryable: true},
		{status: http.StatusInternalServerError, retryable: true},
		{status: http.StatusBadGateway, retryable: true},
		{status: http.StatusServiceUnavailable, retryable: true},
		{status: http.StatusBadRequest, retryable: false},
		{status: http.StatusUnauthorized, retryable: false},
		{status: http.StatusForbidden, retryable: false},
		{status: http.StatusNotFound, retryable: false},
		{status: http.StatusUnprocessableEntity, retryable: false},
	}

	for _, tt := range tests {
		err := &ProviderError{StatusCode: tt.status}
		if got := err.Retryable(); got != tt.retryable {
			t.Errorf("Retryable() for %d = %t, want %t", tt.status, got, tt.retryable)
		}
	}
}

func TestEmbedReturnsATypedProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit reached"}}`))
	}))
	defer server.Close()

	client, err := NewOpenAIClient("test-key")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	client.endpoint = server.URL

	_, err = client.Embed(context.Background(), []string{"input"})
	if err == nil {
		t.Fatal("Embed() error = nil, want a provider error")
	}

	var providerError *ProviderError
	if !errors.As(err, &providerError) {
		t.Fatalf("Embed() error = %v, want it to unwrap to a ProviderError", err)
	}
	if providerError.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want %d", providerError.StatusCode, http.StatusTooManyRequests)
	}
	if !providerError.Retryable() {
		t.Error("Retryable() = false, want true for a rate limit")
	}
}
