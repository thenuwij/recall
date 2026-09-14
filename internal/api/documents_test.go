package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func (s *memoryStore) listDocuments(_ context.Context, _ string) ([]documentSummary, error) {
	if s.err != nil {
		return nil, s.err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var documents []documentSummary
	for _, doc := range s.documents {
		documents = append(documents, documentSummary{
			ID:          doc.ID,
			Title:       doc.Title,
			SourceType:  doc.SourceType,
			Locked:      s.locked[doc.ID],
			Status:      doc.Status,
			CardsStatus: doc.CardsStatus,
			CardCount:   doc.CardCount,
			CreatedAt:   doc.CreatedAt,
		})
	}
	sort.Slice(documents, func(i, j int) bool {
		return documents[i].CreatedAt.After(documents[j].CreatedAt)
	})
	return documents, nil
}

func (s *memoryStore) deleteDocument(_ context.Context, id, _ string) error {
	if s.err != nil {
		return s.err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.documents[id]; !ok {
		return errDocumentNotFound
	}
	if s.locked[id] {
		return errDocumentLocked
	}
	delete(s.documents, id)
	delete(s.chunks, id)
	return nil
}

func (s *memoryStore) chunkLocation(_ context.Context, chunkID, _ string) (chunkLocation, error) {
	if s.err != nil {
		return chunkLocation{}, s.err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	location, ok := s.locations[chunkID]
	if !ok {
		return chunkLocation{}, errChunkNotFound
	}
	return location, nil
}

func intPointer(value int) *int {
	return &value
}

const (
	testDocumentUUID = "0922cc91-c327-45e6-b38b-de38e208ddc7"
	testChunkUUID    = "5b1f3c2e-8d4a-4f6b-9c7e-1a2b3c4d5e6f"
)

func serve(store *memoryStore, method, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	response := httptest.NewRecorder()
	signIn(store, request)
	newTestHandler(store).ServeHTTP(response, request)
	return response
}

func TestListDocuments(t *testing.T) {
	store := newMemoryStore()
	older := time.Date(2026, time.September, 1, 9, 0, 0, 0, time.UTC)
	store.documents["a"] = document{ID: "a", Title: "Week 1", SourceType: sourceText, Status: statusReady, CreatedAt: older}
	store.documents["b"] = document{ID: "b", Title: "Week 2", SourceType: sourcePDF, Status: statusQueued, CreatedAt: older.Add(time.Hour)}

	response := serve(store, http.MethodGet, "/documents")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var result documentListResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Documents) != 2 || result.Documents[0].ID != "b" || result.Documents[1].ID != "a" {
		t.Fatalf("documents = %+v, want b then a, newest first", result.Documents)
	}
	if result.Documents[0].Status != statusQueued || result.Documents[0].SourceType != sourcePDF {
		t.Fatalf("first document = %+v", result.Documents[0])
	}
}

func TestListDocumentsWhenEmpty(t *testing.T) {
	response := serve(newMemoryStore(), http.MethodGet, "/documents")

	if got := response.Body.String(); got != "{\"documents\":[]}\n" {
		t.Fatalf("body = %q, want an empty array rather than null", got)
	}
}

func TestListDocumentsHandlesStoreFailure(t *testing.T) {
	store := newMemoryStore()
	store.err = errors.New("database unavailable")

	if response := serve(store, http.MethodGet, "/documents"); response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}

func TestDeleteDocument(t *testing.T) {
	store := newMemoryStore()
	store.documents[testDocumentUUID] = document{ID: testDocumentUUID}

	response := serve(store, http.MethodDelete, "/documents/"+testDocumentUUID)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", response.Body.String())
	}
	if _, ok := store.documents[testDocumentUUID]; ok {
		t.Fatal("document still stored after delete")
	}
}

func TestDeleteDocumentErrors(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		storeErr   error
		wantStatus int
	}{
		{name: "invalid id", id: "not-a-uuid", wantStatus: http.StatusBadRequest},
		{name: "unknown id", id: testDocumentUUID, wantStatus: http.StatusNotFound},
		{name: "store failure", id: testDocumentUUID, storeErr: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			store.err = tt.storeErr

			if response := serve(store, http.MethodDelete, "/documents/"+tt.id); response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
		})
	}
}

func TestChunkContextForPDFPage(t *testing.T) {
	content := "Page one text.\fThe heart has four chambers. Valves stay closed.\fPage three."
	start := strings.Index(content, "Valves")
	end := start + len("Valves stay closed.")

	store := newMemoryStore()
	store.locations[testChunkUUID] = chunkLocation{
		DocumentID: testDocumentUUID,
		Title:      "Lecture 3",
		SourceType: sourcePDF,
		Content:    content,
		Page:       intPointer(2),
		Start:      &start,
		End:        &end,
	}

	response := serve(store, http.MethodGet, "/chunks/"+testChunkUUID+"/context")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	var result passageContextResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := passageContextResponse{
		DocumentID: testDocumentUUID,
		Title:      "Lecture 3",
		Page:       intPointer(2),
		Before:     "The heart has four chambers. ",
		Passage:    "Valves stay closed.",
		After:      "",
	}
	if result.DocumentID != want.DocumentID || result.Title != want.Title || result.Page == nil || *result.Page != 2 ||
		result.Before != want.Before || result.Passage != want.Passage || result.After != want.After {
		t.Fatalf("context = %+v, want %+v", result, want)
	}
}

func TestChunkContextErrors(t *testing.T) {
	start, end := 0, 4
	tests := []struct {
		name       string
		id         string
		location   *chunkLocation
		wantStatus int
	}{
		{name: "invalid id", id: "not-a-uuid", wantStatus: http.StatusBadRequest},
		{name: "unknown chunk", id: testChunkUUID, wantStatus: http.StatusNotFound},
		{
			name:       "chunk from before source locations",
			id:         testChunkUUID,
			location:   &chunkLocation{Content: "old text"},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "span outside the content",
			id:         testChunkUUID,
			location:   &chunkLocation{Content: "abc", Start: &start, End: &end},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			if tt.location != nil {
				store.locations[testChunkUUID] = *tt.location
			}

			if response := serve(store, http.MethodGet, "/chunks/"+tt.id+"/context"); response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.wantStatus, response.Body.String())
			}
		})
	}
}

func TestPassageContext(t *testing.T) {
	long := strings.Repeat("word ", 150)
	tests := []struct {
		name       string
		content    string
		passage    string
		paged      bool
		wantBefore string
		wantAfter  string
	}{
		{
			name:       "first pdf page",
			content:    "Alpha beta.\fGamma.",
			passage:    "beta.",
			paged:      true,
			wantBefore: "Alpha ",
			wantAfter:  "",
		},
		{
			name:       "last pdf page without trailing form feed",
			content:    "One.\fTwo three four",
			passage:    "three",
			paged:      true,
			wantBefore: "Two ",
			wantAfter:  " four",
		},
		{
			name:       "short text document is shown whole",
			content:    "Before the passage. PASSAGE here. After it.",
			passage:    "PASSAGE here.",
			wantBefore: "Before the passage. ",
			wantAfter:  " After it.",
		},
		{
			name:       "long text document is cut at word boundaries",
			content:    long + "PASSAGE" + " " + long,
			passage:    "PASSAGE",
			wantBefore: " " + strings.Repeat("word ", 99),
			wantAfter:  " " + strings.Repeat("word ", 98) + "word",
		},
		{
			name:       "no whitespace inside the cut drops that side",
			content:    strings.Repeat("x", 600) + " PASSAGE " + strings.Repeat("y", 600),
			passage:    "PASSAGE",
			wantBefore: " ",
			wantAfter:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := strings.Index(tt.content, tt.passage)
			end := start + len(tt.passage)

			before, passage, after := passageContext(tt.content, start, end, tt.paged)

			if passage != tt.passage {
				t.Fatalf("passage = %q, want %q", passage, tt.passage)
			}
			if !strings.HasSuffix(tt.content[:start], before) || !strings.HasPrefix(tt.content[end:], after) {
				t.Fatalf("before and after are not the text adjacent to the passage")
			}
			if before != tt.wantBefore {
				t.Errorf("before = %q, want %q", before, tt.wantBefore)
			}
			if after != tt.wantAfter {
				t.Errorf("after = %q, want %q", after, tt.wantAfter)
			}
		})
	}
}

func TestPassageContextNeverSplitsMultiByteCharacters(t *testing.T) {
	content := strings.Repeat("é", 400) + " café PASSAGE " + strings.Repeat("α", 400)
	start := strings.Index(content, "PASSAGE")
	end := start + len("PASSAGE")

	before, _, after := passageContext(content, start, end, false)

	if before != " café " {
		t.Errorf("before = %q, want %q", before, " café ")
	}
	if after != "" {
		t.Errorf("after = %q, want empty: the cut lands inside a word", after)
	}
	if !utf8.ValidString(before) || !utf8.ValidString(after) {
		t.Fatalf("context split a multi-byte character: before=%q after=%q", before, after)
	}
}

func TestAnswerCitationsIncludeTitleAndPage(t *testing.T) {
	store := newMemoryStore()
	store.searchResults = evidence(0.61)
	store.searchResults[0].Title = "Lecture 3"
	store.searchResults[0].Page = intPointer(14)
	gen := &fakeGenerator{answer: "Both valves stay closed [1]."}

	decoded := decodeAnswer(t, postAnswer(t, store, gen, `{"query":"a"}`))

	if len(decoded.Citations) != 1 {
		t.Fatalf("citations = %d, want 1", len(decoded.Citations))
	}
	citation := decoded.Citations[0]
	if citation.Title != "Lecture 3" || citation.Page == nil || *citation.Page != 14 {
		t.Fatalf("citation = %+v, want title Lecture 3 on page 14", citation)
	}
}

func TestDeleteDocumentRefusesALockedDocument(t *testing.T) {
	store := newMemoryStore()
	store.documents[testDocumentUUID] = document{ID: testDocumentUUID}
	store.locked = map[string]bool{testDocumentUUID: true}

	response := serve(store, http.MethodDelete, "/documents/"+testDocumentUUID)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if _, ok := store.documents[testDocumentUUID]; !ok {
		t.Fatal("locked document was deleted")
	}
}

func TestListDocumentsReportsLockedDocuments(t *testing.T) {
	store := newMemoryStore()
	store.documents["locked"] = document{ID: "locked", CreatedAt: time.Date(2026, time.September, 1, 9, 0, 0, 0, time.UTC)}
	store.documents["open"] = document{ID: "open", CreatedAt: time.Date(2026, time.September, 2, 9, 0, 0, 0, time.UTC)}
	store.locked = map[string]bool{"locked": true}

	response := serve(store, http.MethodGet, "/documents")

	var result documentListResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Documents) != 2 || result.Documents[0].Locked || !result.Documents[1].Locked {
		t.Fatalf("documents = %+v, want open unlocked and locked locked", result.Documents)
	}
}
