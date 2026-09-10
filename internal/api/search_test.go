package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thenujawijesuriya/recall/internal/embedding"
)

func postSearch(t *testing.T, store *memoryStore, embedder embeddingGenerator, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(body))
	response := httptest.NewRecorder()
	NewHandler(store, embedder, &fakeGenerator{}, &fakePublisher{}).ServeHTTP(response, request)
	return response
}

func TestSearchReturnsResults(t *testing.T) {
	store := newMemoryStore()
	store.searchResults = []searchResult{
		{ChunkID: "chunk-a", DocumentID: "doc-a", ChunkIndex: 0, Content: "first", Similarity: 0.92},
		{ChunkID: "chunk-b", DocumentID: "doc-b", ChunkIndex: 3, Content: "second", Similarity: 0.71},
	}

	response := postSearch(t, store, &fakeEmbedder{}, `{"query":"how does Recall preserve context?"}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}

	var decoded searchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(decoded.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(decoded.Results))
	}
	if got := decoded.Results[0]; got.ChunkID != "chunk-a" || got.DocumentID != "doc-a" ||
		got.ChunkIndex != 0 || got.Content != "first" || got.Similarity != 0.92 {
		t.Fatalf("first result = %+v", got)
	}
	if decoded.Results[1].ChunkID != "chunk-b" {
		t.Fatalf("second result = %+v, want chunk-b", decoded.Results[1])
	}
}

func TestSearchEmbedsTrimmedQueryWithConfiguredModel(t *testing.T) {
	store := newMemoryStore()

	response := postSearch(t, store, &fakeEmbedder{}, `{"query":"  vector search  "}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if len(store.searchCalls) != 1 {
		t.Fatalf("searchChunks called %d times, want 1", len(store.searchCalls))
	}

	call := store.searchCalls[0]
	if len(call.embedding) != embedding.Dimensions {
		t.Fatalf("query embedding dimensions = %d, want %d", len(call.embedding), embedding.Dimensions)
	}
	if call.model != embedding.Model {
		t.Fatalf("model = %q, want %q", call.model, embedding.Model)
	}
}

func TestSearchLimits(t *testing.T) {
	testCases := []struct {
		name      string
		body      string
		wantLimit int
	}{
		{name: "omitted uses default", body: `{"query":"a"}`, wantLimit: defaultSearchLimit},
		{name: "null uses default", body: `{"query":"a","limit":null}`, wantLimit: defaultSearchLimit},
		{name: "explicit value", body: `{"query":"a","limit":3}`, wantLimit: 3},
		{name: "lower boundary", body: `{"query":"a","limit":1}`, wantLimit: 1},
		{name: "upper boundary", body: `{"query":"a","limit":20}`, wantLimit: maxSearchLimit},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newMemoryStore()

			response := postSearch(t, store, &fakeEmbedder{}, testCase.body)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
			}
			if len(store.searchCalls) != 1 {
				t.Fatalf("searchChunks called %d times, want 1", len(store.searchCalls))
			}
			if got := store.searchCalls[0].limit; got != testCase.wantLimit {
				t.Fatalf("limit = %d, want %d", got, testCase.wantLimit)
			}
		})
	}
}

func TestSearchRejectsInvalidRequests(t *testing.T) {
	testCases := []struct {
		name string
		body string
	}{
		{name: "empty query", body: `{"query":""}`},
		{name: "whitespace-only query", body: `{"query":"   \n\t "}`},
		{name: "missing query field", body: `{}`},
		{name: "malformed JSON", body: `{"query":`},
		{name: "unknown field", body: `{"query":"a","top_k":5}`},
		{name: "trailing JSON after object", body: `{"query":"a"}{"query":"b"}`},
		{name: "wrong query type", body: `{"query":123}`},
		{name: "zero limit", body: `{"query":"a","limit":0}`},
		{name: "negative limit", body: `{"query":"a","limit":-1}`},
		{name: "limit above maximum", body: `{"query":"a","limit":21}`},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newMemoryStore()

			response := postSearch(t, store, &fakeEmbedder{}, testCase.body)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			if len(store.searchCalls) != 0 {
				t.Fatalf("searchChunks called %d times, want 0", len(store.searchCalls))
			}
		})
	}
}

func TestSearchRejectsOversizedBody(t *testing.T) {
	store := newMemoryStore()
	body := `{"query":"` + strings.Repeat("a", maxRequestBodyBytes+1) + `"}`

	response := postSearch(t, store, &fakeEmbedder{}, body)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestSearchEncodesEmptyResultsAsArray(t *testing.T) {
	store := newMemoryStore()
	store.searchResults = nil

	response := postSearch(t, store, &fakeEmbedder{}, `{"query":"nothing matches"}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got, want := response.Body.String(), "{\"results\":[]}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestSearchHandlesEmbeddingFailure(t *testing.T) {
	store := newMemoryStore()
	embedder := &fakeEmbedder{err: errors.New("provider unavailable")}

	response := postSearch(t, store, embedder, `{"query":"a"}`)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	if len(store.searchCalls) != 0 {
		t.Fatalf("searchChunks called %d times, want 0", len(store.searchCalls))
	}
}

type shortVectorEmbedder struct{}

func (shortVectorEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	result := make([][]float32, len(inputs))
	for index := range inputs {
		result[index] = make([]float32, embedding.Dimensions-1)
	}
	return result, nil
}

func TestSearchRejectsWrongDimensionEmbedding(t *testing.T) {
	store := newMemoryStore()

	response := postSearch(t, store, shortVectorEmbedder{}, `{"query":"a"}`)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	if len(store.searchCalls) != 0 {
		t.Fatalf("searchChunks called %d times, want 0", len(store.searchCalls))
	}
}

func TestSearchHandlesStoreFailure(t *testing.T) {
	store := newMemoryStore()
	store.searchErr = errors.New("connection refused")

	response := postSearch(t, store, &fakeEmbedder{}, `{"query":"a"}`)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}
