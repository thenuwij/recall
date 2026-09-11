package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func evidence(similarity ...float64) []searchResult {
	results := make([]searchResult, 0, len(similarity))
	for index, score := range similarity {
		results = append(results, searchResult{
			ChunkID:    "chunk-" + string(rune('a'+index)),
			DocumentID: "doc-" + string(rune('a'+index)),
			ChunkIndex: index,
			Content:    "passage " + string(rune('a'+index)),
			Similarity: score,
		})
	}
	return results
}

func postAnswer(t *testing.T, store *memoryStore, gen *fakeGenerator, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/answer", strings.NewReader(body))
	response := httptest.NewRecorder()
	NewHandler(store, &fakeEmbedder{}, gen, &fakePublisher{}, &fakeExtractor{}).ServeHTTP(response, request)
	return response
}

func decodeAnswer(t *testing.T, response *httptest.ResponseRecorder) answerResponse {
	t.Helper()

	var decoded answerResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return decoded
}

func TestAnswerReturnsGroundedAnswerWithCitations(t *testing.T) {
	store := newMemoryStore()
	store.searchResults = evidence(0.61, 0.44)
	gen := &fakeGenerator{answer: "Writes are grouped into transactions [1], and the log survives restarts [2]."}

	response := postAnswer(t, store, gen, `{"query":"how is data protected?"}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}

	decoded := decodeAnswer(t, response)
	if decoded.Refused {
		t.Fatalf("refused = true, want false")
	}
	if len(decoded.Citations) != 2 {
		t.Fatalf("citations = %d, want 2: %+v", len(decoded.Citations), decoded.Citations)
	}
	if got := decoded.Citations[0]; got.Marker != 1 || got.ChunkID != "chunk-a" || got.Similarity != 0.61 {
		t.Fatalf("first citation = %+v", got)
	}
	if got := decoded.Citations[1]; got.Marker != 2 || got.ChunkID != "chunk-b" {
		t.Fatalf("second citation = %+v", got)
	}
}

func TestAnswerPassesNumberedPassagesToGenerator(t *testing.T) {
	store := newMemoryStore()
	store.searchResults = evidence(0.61, 0.44)
	gen := &fakeGenerator{answer: "grounded [1]"}

	postAnswer(t, store, gen, `{"query":"  how is data protected?  "}`)

	if len(gen.calls) != 1 {
		t.Fatalf("generator called %d times, want 1", len(gen.calls))
	}
	call := gen.calls[0]
	if call.query != "how is data protected?" {
		t.Fatalf("query = %q, want trimmed query", call.query)
	}
	if len(call.passages) != 2 {
		t.Fatalf("passages = %d, want 2", len(call.passages))
	}
	if call.passages[0].Marker != 1 || call.passages[1].Marker != 2 {
		t.Fatalf("markers = %d,%d, want 1,2", call.passages[0].Marker, call.passages[1].Marker)
	}
	if call.passages[0].Content != "passage a" {
		t.Fatalf("first passage content = %q", call.passages[0].Content)
	}
}

func TestAnswerReturnsOnlyCitedPassages(t *testing.T) {
	store := newMemoryStore()
	store.searchResults = evidence(0.61, 0.44, 0.33)
	gen := &fakeGenerator{answer: "Only the third passage matters [3]."}

	decoded := decodeAnswer(t, postAnswer(t, store, gen, `{"query":"a"}`))

	if len(decoded.Citations) != 1 {
		t.Fatalf("citations = %d, want 1", len(decoded.Citations))
	}
	if decoded.Citations[0].Marker != 3 || decoded.Citations[0].ChunkID != "chunk-c" {
		t.Fatalf("citation = %+v", decoded.Citations[0])
	}
}

func TestAnswerDeduplicatesAndOrdersMarkers(t *testing.T) {
	store := newMemoryStore()
	store.searchResults = evidence(0.61, 0.44)
	gen := &fakeGenerator{answer: "Second [2], then first [1], then second again [2]."}

	decoded := decodeAnswer(t, postAnswer(t, store, gen, `{"query":"a"}`))

	if len(decoded.Citations) != 2 {
		t.Fatalf("citations = %d, want 2", len(decoded.Citations))
	}
	if decoded.Citations[0].Marker != 1 || decoded.Citations[1].Marker != 2 {
		t.Fatalf("markers = %d,%d, want 1,2 ascending", decoded.Citations[0].Marker, decoded.Citations[1].Marker)
	}
}

func TestAnswerRefusesWhenCorpusIsEmpty(t *testing.T) {
	store := newMemoryStore()
	store.searchResults = nil
	gen := &fakeGenerator{answer: "should never be produced"}

	response := postAnswer(t, store, gen, `{"query":"anything"}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	decoded := decodeAnswer(t, response)
	if !decoded.Refused || decoded.Reason == "" {
		t.Fatalf("expected a refusal with a reason, got %+v", decoded)
	}
	if len(gen.calls) != 0 {
		t.Fatalf("generator called %d times, want 0", len(gen.calls))
	}
}

func TestAnswerRefusesWhenEvidenceIsTooWeak(t *testing.T) {
	store := newMemoryStore()
	store.searchResults = evidence(minAnswerSimilarity-0.01, 0.05)
	gen := &fakeGenerator{answer: "should never be produced"}

	response := postAnswer(t, store, gen, `{"query":"unrelated question"}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	decoded := decodeAnswer(t, response)
	if !decoded.Refused {
		t.Fatalf("refused = false, want true: %+v", decoded)
	}
	if decoded.Answer != "" || len(decoded.Citations) != 0 {
		t.Fatalf("refusal must carry no answer and no citations: %+v", decoded)
	}
	if len(gen.calls) != 0 {
		t.Fatalf("generator called %d times, want 0 - the gate must run before the model", len(gen.calls))
	}
}

func TestAnswerAcceptsEvidenceExactlyOnTheFloor(t *testing.T) {
	store := newMemoryStore()
	store.searchResults = evidence(minAnswerSimilarity)
	gen := &fakeGenerator{answer: "grounded [1]"}

	decoded := decodeAnswer(t, postAnswer(t, store, gen, `{"query":"a"}`))

	if decoded.Refused {
		t.Fatalf("similarity exactly at the floor must be accepted: %+v", decoded)
	}
}

func TestAnswerRefusesWhenModelCitesNothing(t *testing.T) {
	testCases := []struct {
		name   string
		answer string
	}{
		{name: "uncited prose", answer: "Data is protected by transactions."},
		{name: "empty answer", answer: ""},
		{name: "whitespace only", answer: "   \n  "},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newMemoryStore()
			store.searchResults = evidence(0.61)
			gen := &fakeGenerator{answer: testCase.answer}

			response := postAnswer(t, store, gen, `{"query":"a"}`)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if decoded := decodeAnswer(t, response); !decoded.Refused {
				t.Fatalf("an answer with no citations must be refused: %+v", decoded)
			}
		})
	}
}

func TestAnswerRejectsFabricatedCitations(t *testing.T) {
	testCases := []struct {
		name   string
		answer string
	}{
		{name: "marker above range", answer: "Claim [3]."},
		{name: "marker zero", answer: "Claim [0]."},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newMemoryStore()
			store.searchResults = evidence(0.61, 0.44)
			gen := &fakeGenerator{answer: testCase.answer}

			response := postAnswer(t, store, gen, `{"query":"a"}`)

			if response.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusBadGateway, response.Body.String())
			}
		})
	}
}

func TestAnswerHandlesGeneratorFailure(t *testing.T) {
	store := newMemoryStore()
	store.searchResults = evidence(0.61)
	gen := &fakeGenerator{err: errors.New("provider unavailable")}

	response := postAnswer(t, store, gen, `{"query":"a"}`)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
}

func TestAnswerHandlesStoreFailure(t *testing.T) {
	store := newMemoryStore()
	store.searchErr = errors.New("connection refused")
	gen := &fakeGenerator{}

	response := postAnswer(t, store, gen, `{"query":"a"}`)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if len(gen.calls) != 0 {
		t.Fatalf("generator called %d times, want 0", len(gen.calls))
	}
}

func TestAnswerHandlesEmbeddingFailure(t *testing.T) {
	store := newMemoryStore()
	gen := &fakeGenerator{}
	request := httptest.NewRequest(http.MethodPost, "/answer", strings.NewReader(`{"query":"a"}`))
	response := httptest.NewRecorder()

	NewHandler(store, &fakeEmbedder{err: errors.New("provider unavailable")}, gen, &fakePublisher{}, &fakeExtractor{}).ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	if len(store.searchCalls) != 0 || len(gen.calls) != 0 {
		t.Fatalf("no downstream call should be made after an embedding failure")
	}
}

func TestAnswerLimits(t *testing.T) {
	testCases := []struct {
		name      string
		body      string
		wantLimit int
	}{
		{name: "omitted uses default", body: `{"query":"a"}`, wantLimit: defaultAnswerLimit},
		{name: "null uses default", body: `{"query":"a","limit":null}`, wantLimit: defaultAnswerLimit},
		{name: "explicit value", body: `{"query":"a","limit":3}`, wantLimit: 3},
		{name: "lower boundary", body: `{"query":"a","limit":1}`, wantLimit: 1},
		{name: "upper boundary", body: `{"query":"a","limit":10}`, wantLimit: maxAnswerLimit},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newMemoryStore()
			store.searchResults = evidence(0.61)
			gen := &fakeGenerator{answer: "grounded [1]"}

			postAnswer(t, store, gen, testCase.body)

			if len(store.searchCalls) != 1 {
				t.Fatalf("searchChunks called %d times, want 1", len(store.searchCalls))
			}
			if got := store.searchCalls[0].limit; got != testCase.wantLimit {
				t.Fatalf("limit = %d, want %d", got, testCase.wantLimit)
			}
		})
	}
}

func TestAnswerRejectsInvalidRequests(t *testing.T) {
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
		{name: "limit above maximum", body: `{"query":"a","limit":11}`},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newMemoryStore()
			gen := &fakeGenerator{}

			response := postAnswer(t, store, gen, testCase.body)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			if len(store.searchCalls) != 0 || len(gen.calls) != 0 {
				t.Fatalf("an invalid request must not reach the database or the model")
			}
		})
	}
}

func TestAnswerRejectsOversizedBody(t *testing.T) {
	store := newMemoryStore()
	gen := &fakeGenerator{}
	body := `{"query":"` + strings.Repeat("a", maxRequestBodyBytes+1) + `"}`

	response := postAnswer(t, store, gen, body)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestAnswerRefusalEncodesCitationsAsArray(t *testing.T) {
	store := newMemoryStore()
	store.searchResults = nil
	gen := &fakeGenerator{}

	response := postAnswer(t, store, gen, `{"query":"a"}`)

	if strings.Contains(response.Body.String(), `"citations":null`) {
		t.Fatalf("citations must encode as [] not null: %s", response.Body.String())
	}
}
