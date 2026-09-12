package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func intValue(value int) *int {
	return &value
}

func TestSubmitDocumentRejectsWhenTheDocumentQuotaIsReached(t *testing.T) {
	store := newMemoryStore()
	store.documents["existing"] = document{ID: "existing"}

	request := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(`{"content":"one more document"}`))
	signInWithQuota(store, request, intValue(1), intValue(50))
	response := httptest.NewRecorder()

	newTestHandler(store).ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (%s)", response.Code, http.StatusForbidden, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "document limit reached") {
		t.Fatalf("body = %q, want the document limit message", response.Body.String())
	}
}

func TestSubmitDocumentAllowsAnAccountBelowItsQuota(t *testing.T) {
	store := newMemoryStore()

	request := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(`{"content":"first document"}`))
	signInWithQuota(store, request, intValue(1), intValue(50))
	response := httptest.NewRecorder()

	newTestHandler(store).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (%s)", response.Code, http.StatusAccepted, response.Body.String())
	}
}

func TestSubmitDocumentAllowsAnUnlimitedAccount(t *testing.T) {
	store := newMemoryStore()
	for index := range 20 {
		store.documents[string(rune('a'+index))] = document{}
	}

	request := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(`{"content":"another document"}`))
	signInWithQuota(store, request, nil, nil)
	response := httptest.NewRecorder()

	newTestHandler(store).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (%s)", response.Code, http.StatusAccepted, response.Body.String())
	}
}

func TestUploadRejectsWhenTheDocumentExceedsThePageQuota(t *testing.T) {
	store := newMemoryStore()
	extractor := &fakeExtractor{text: "page one\fpage two\fpage three"}

	request := uploadRequest(t, "file", "lecture.pdf", []byte("%PDF-1.4 fixture"))
	signInWithQuota(store, request, intValue(10), intValue(2))

	response := httptest.NewRecorder()
	NewHandler(store, &fakeEmbedder{}, &fakeGenerator{}, &fakePublisher{}, extractor).ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (%s)", response.Code, http.StatusForbidden, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "page limit reached") {
		t.Fatalf("body = %q, want the page limit message", response.Body.String())
	}
}

func TestUploadAllowsADocumentWithinThePageQuota(t *testing.T) {
	store := newMemoryStore()
	extractor := &fakeExtractor{text: "page one\fpage two"}

	request := uploadRequest(t, "file", "lecture.pdf", []byte("%PDF-1.4 fixture"))
	signInWithQuota(store, request, intValue(10), intValue(2))

	response := httptest.NewRecorder()
	NewHandler(store, &fakeEmbedder{}, &fakeGenerator{}, &fakePublisher{}, extractor).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (%s)", response.Code, http.StatusAccepted, response.Body.String())
	}
}
