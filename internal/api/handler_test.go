package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thenujawijesuriya/recall/internal/embedding"
	"github.com/thenujawijesuriya/recall/internal/generation"
)

type memoryStore struct {
	mu        sync.RWMutex
	documents map[string]document
	chunks    map[string][]string
	err       error

	searchResults []searchResult
	searchErr     error
	searchCalls   []searchCall
}

type searchCall struct {
	embedding []float32
	model     string
	limit     int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		documents: make(map[string]document),
		chunks:    make(map[string][]string),
	}
}

func (s *memoryStore) createDocument(_ context.Context, content string, chunks []string) (string, string, error) {
	if s.err != nil {
		return "", "", s.err
	}

	const id = "test-document-id"
	s.mu.Lock()
	s.documents[id] = document{ID: id, Content: content, Status: statusQueued}
	s.chunks[id] = append([]string(nil), chunks...)
	s.mu.Unlock()
	return id, "test-job-id", nil
}

type fakePublisher struct {
	err       error
	published []string
}

func (p *fakePublisher) Publish(_ context.Context, jobID string) error {
	p.published = append(p.published, jobID)
	return p.err
}

type fakeEmbedder struct {
	err   error
	calls int
}

func (e *fakeEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}

	result := make([][]float32, len(inputs))
	for index := range inputs {
		result[index] = make([]float32, embedding.Dimensions)
		result[index][0] = float32(index + 1)
	}
	return result, nil
}

func (s *memoryStore) searchChunks(_ context.Context, queryEmbedding []float32, model string, limit int) ([]searchResult, error) {
	s.mu.Lock()
	s.searchCalls = append(s.searchCalls, searchCall{
		embedding: append([]float32(nil), queryEmbedding...),
		model:     model,
		limit:     limit,
	})
	s.mu.Unlock()

	if s.searchErr != nil {
		return nil, s.searchErr
	}

	return s.searchResults, nil
}

type generatorCall struct {
	query    string
	passages []generation.Passage
}

type fakeGenerator struct {
	answer string
	err    error
	calls  []generatorCall
}

func (g *fakeGenerator) GenerateAnswer(_ context.Context, query string, passages []generation.Passage) (string, error) {
	g.calls = append(g.calls, generatorCall{
		query:    query,
		passages: append([]generation.Passage(nil), passages...),
	})
	if g.err != nil {
		return "", g.err
	}
	return g.answer, nil
}

func newTestHandler(store documentStore) http.Handler {
	return NewHandler(store, &fakeEmbedder{}, &fakeGenerator{}, &fakePublisher{})
}

func (s *memoryStore) getDocument(_ context.Context, id string) (document, error) {
	if s.err != nil {
		return document{}, s.err
	}

	s.mu.RLock()
	result, ok := s.documents[id]
	s.mu.RUnlock()
	if !ok {
		return document{}, errDocumentNotFound
	}

	return result, nil
}

func TestHealth(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	newTestHandler(newMemoryStore()).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Body.String(); got != "{\"status\":\"ok\"}\n" {
		t.Fatalf("body = %q", got)
	}
}

func TestSubmitDocumentCallsStore(t *testing.T) {
	store := newMemoryStore()
	request := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(`{"content":"stored temporarily"}`))
	response := httptest.NewRecorder()

	newTestHandler(store).ServeHTTP(response, request)

	var result submitDocumentResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Status != statusQueued {
		t.Errorf("status = %q, want %q", result.Status, statusQueued)
	}

	store.mu.RLock()
	storedContent := store.documents[result.ID].Content
	storedChunks := append([]string(nil), store.chunks[result.ID]...)
	store.mu.RUnlock()
	if storedContent != "stored temporarily" {
		t.Fatalf("stored content = %q, want %q", storedContent, "stored temporarily")
	}
	if len(storedChunks) != 1 {
		t.Fatalf("stored chunk count = %d, want 1", len(storedChunks))
	}
	if storedChunks[0] != "stored temporarily" {
		t.Fatalf("stored chunk content = %q, want %q", storedChunks[0], "stored temporarily")
	}
}

func TestSubmitDocumentDoesNotEmbedOnTheRequestPath(t *testing.T) {
	store := newMemoryStore()
	embedder := &fakeEmbedder{}
	request := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(`{"content":"deferred work"}`))
	response := httptest.NewRecorder()

	NewHandler(store, embedder, &fakeGenerator{}, &fakePublisher{}).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusAccepted)
	}
	if embedder.calls != 0 {
		t.Errorf("embedder calls = %d, want 0: embedding belongs to the worker", embedder.calls)
	}
}

func TestSubmitDocument(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "valid document",
			body:       `{"content":"Go handlers turn HTTP requests into responses."}`,
			wantStatus: http.StatusAccepted,
			wantBody:   `{"id":"`,
		},
		{
			name:       "malformed JSON",
			body:       `{"content":`,
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"request body must contain valid JSON with a content field"}`,
		},
		{
			name:       "empty content",
			body:       `{"content":"   "}`,
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"error":"content must not be empty"}`,
		},
		{
			name:       "oversized request",
			body:       `{"content":"` + strings.Repeat("a", maxRequestBodyBytes) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   `{"error":"request body must not exceed 1 MiB"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(tt.body))
			response := httptest.NewRecorder()

			newTestHandler(newMemoryStore()).ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.wantStatus, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), tt.wantBody) {
				t.Fatalf("body = %q, want it to contain %q", response.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestSubmitDocumentHandlesStoreFailure(t *testing.T) {
	store := newMemoryStore()
	store.err = errors.New("database unavailable")
	request := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(`{"content":"valid content"}`))
	response := httptest.NewRecorder()

	newTestHandler(store).ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if got := response.Body.String(); got != "{\"error\":\"could not store document\"}\n" {
		t.Fatalf("body = %q", got)
	}
}

func TestGetDocument(t *testing.T) {
	const id = "0922cc91-c327-45e6-b38b-de38e208ddc7"
	createdAt := time.Date(2026, time.September, 7, 7, 19, 52, 0, time.UTC)
	store := newMemoryStore()
	store.documents[id] = document{
		ID:        id,
		Content:   "This document was stored through the Recall API.",
		CreatedAt: createdAt,
		Status:    statusReady,
	}
	request := httptest.NewRequest(http.MethodGet, "/documents/"+id, nil)
	response := httptest.NewRecorder()

	newTestHandler(store).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	wantBody := "{\"id\":\"0922cc91-c327-45e6-b38b-de38e208ddc7\",\"content\":\"This document was stored through the Recall API.\",\"created_at\":\"2026-09-07T07:19:52Z\",\"status\":\"ready\"}\n"
	if got := response.Body.String(); got != wantBody {
		t.Fatalf("body = %q, want %q", got, wantBody)
	}
}

func TestGetDocumentReportsIngestionFailureReason(t *testing.T) {
	const id = "0922cc91-c327-45e6-b38b-de38e208ddc7"
	store := newMemoryStore()
	store.documents[id] = document{
		ID:      id,
		Content: "This document could not be embedded.",
		Status:  statusFailed,
		Reason:  "provider unavailable after 3 attempts",
	}
	request := httptest.NewRequest(http.MethodGet, "/documents/"+id, nil)
	response := httptest.NewRecorder()

	newTestHandler(store).ServeHTTP(response, request)

	var result document
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Status != statusFailed {
		t.Errorf("status = %q, want %q", result.Status, statusFailed)
	}
	if result.Reason != "provider unavailable after 3 attempts" {
		t.Errorf("reason = %q, want the recorded failure reason", result.Reason)
	}
}

func TestIngestionStatusMapsJobStates(t *testing.T) {
	tests := []struct {
		state *string
		want  string
	}{
		{state: nil, want: statusUnknown},
		{state: ptr(jobQueued), want: statusQueued},
		{state: ptr(jobProcessing), want: statusProcessing},
		{state: ptr(jobCompleted), want: statusReady},
		{state: ptr(jobFailed), want: statusFailed},
		{state: ptr("something else"), want: statusUnknown},
	}

	for _, tt := range tests {
		if got := ingestionStatus(tt.state); got != tt.want {
			t.Errorf("ingestionStatus(%v) = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func ptr(value string) *string {
	return &value
}

func TestGetDocumentErrors(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		storeError error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "invalid UUID",
			id:         "not-a-uuid",
			wantStatus: http.StatusBadRequest,
			wantBody:   "{\"error\":\"document id must be a valid UUID\"}\n",
		},
		{
			name:       "document not found",
			id:         "11111111-1111-1111-1111-111111111111",
			wantStatus: http.StatusNotFound,
			wantBody:   "{\"error\":\"document not found\"}\n",
		},
		{
			name:       "store failure",
			id:         "11111111-1111-1111-1111-111111111111",
			storeError: errors.New("database unavailable"),
			wantStatus: http.StatusInternalServerError,
			wantBody:   "{\"error\":\"could not retrieve document\"}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			store.err = tt.storeError
			request := httptest.NewRequest(http.MethodGet, "/documents/"+tt.id, nil)
			response := httptest.NewRecorder()

			newTestHandler(store).ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.wantStatus, response.Body.String())
			}
			if got := response.Body.String(); got != tt.wantBody {
				t.Fatalf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

func TestSubmitDocumentPublishesTheJob(t *testing.T) {
	store := newMemoryStore()
	publisher := &fakePublisher{}
	request := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(`{"content":"notify the worker"}`))
	response := httptest.NewRecorder()

	NewHandler(store, &fakeEmbedder{}, &fakeGenerator{}, publisher).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusAccepted)
	}
	if len(publisher.published) != 1 {
		t.Fatalf("published = %d, want 1", len(publisher.published))
	}
	if publisher.published[0] != "test-job-id" {
		t.Errorf("published job = %q, want %q", publisher.published[0], "test-job-id")
	}
}

func TestSubmitDocumentSucceedsWhenPublishingFails(t *testing.T) {
	store := newMemoryStore()
	publisher := &fakePublisher{err: errors.New("redis unavailable")}
	request := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(`{"content":"durable regardless"}`))
	response := httptest.NewRecorder()

	NewHandler(store, &fakeEmbedder{}, &fakeGenerator{}, publisher).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: the document is durable even when the notification fails", response.Code, http.StatusAccepted)
	}

	store.mu.RLock()
	stored := len(store.documents)
	store.mu.RUnlock()
	if stored != 1 {
		t.Errorf("stored documents = %d, want 1", stored)
	}
}
