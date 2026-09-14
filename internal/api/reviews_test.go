package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thenujawijesuriya/recall/internal/generation"
	"github.com/thenujawijesuriya/recall/internal/scheduling"
)

const testCardUUID = "7c9e6679-7425-40de-944b-e07fc1f90ae7"

type dueCall struct {
	limit      int
	newCardCap int
}

type gradeCall struct {
	question       string
	expectedAnswer string
	passage        string
	answer         string
}

func (g *fakeGenerator) GradeAnswer(_ context.Context, question, expectedAnswer, sourcePassage, learnerAnswer string) (generation.Grade, error) {
	g.gradeCalls = append(g.gradeCalls, gradeCall{question: question, expectedAnswer: expectedAnswer, passage: sourcePassage, answer: learnerAnswer})
	if g.gradeErr != nil {
		return generation.Grade{}, g.gradeErr
	}
	return g.grade, nil
}

func (s *memoryStore) dueCards(_ context.Context, limit, newCardCap int, _ string, scopes ...reviewScope) ([]dueCard, error) {
	s.dueCalls = append(s.dueCalls, dueCall{limit: limit, newCardCap: newCardCap})
	if s.err != nil {
		return nil, s.err
	}
	return s.due, nil
}

func (s *memoryStore) nextDueAt(_ context.Context, _ string, _ int, scopes ...reviewScope) (*time.Time, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.nextDue, nil
}

func (s *memoryStore) reviewCard(_ context.Context, cardID, _ string) (reviewCard, error) {
	if s.err != nil {
		return reviewCard{}, s.err
	}
	card, ok := s.reviewCards[cardID]
	if !ok {
		return reviewCard{}, errCardNotFound
	}
	return card, nil
}

func (s *memoryStore) recordReview(_ context.Context, review newReview) error {
	if s.reviewErr != nil {
		return s.reviewErr
	}
	s.reviews = append(s.reviews, review)
	return nil
}

func testReviewCard() reviewCard {
	content := "Title page\fBoth valves stay closed during isovolumetric contraction. Pressure rises."
	start := strings.Index(content, "Both")
	end := strings.Index(content, " Pressure")
	return reviewCard{
		Question:       "Why do both valves stay closed?",
		ExpectedAnswer: "Ventricular pressure is between atrial and aortic pressure.",
		Passage:        content[start:end],
		Location: chunkLocation{
			DocumentID: testDocumentUUID,
			Title:      "Cardiac Cycle",
			SourceType: sourcePDF,
			Content:    content,
			Page:       intPointer(2),
			Start:      intPointer(start),
			End:        intPointer(end),
		},
		Schedule:  scheduling.State{EaseFactor: 2.5},
		UpdatedAt: time.Date(2026, time.September, 11, 9, 0, 0, 0, time.UTC),
	}
}

func serveReview(store *memoryStore, generator *fakeGenerator, cardID, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/reviews/"+cardID, strings.NewReader(body))
	response := httptest.NewRecorder()
	signIn(store, request)
	NewHandler(store, &fakeEmbedder{}, generator, &fakePublisher{}, &fakeExtractor{}).ServeHTTP(response, request)
	return response
}

func TestDueReviewsReturnsQuestionsAndNextDueTime(t *testing.T) {
	store := newMemoryStore()
	next := time.Date(2026, time.September, 12, 9, 0, 0, 0, time.UTC)
	store.nextDue = &next
	store.due = []dueCard{
		{CardID: "a", Question: "Due review?", Title: "Week 1", IsNew: false},
		{CardID: "b", Question: "New card?", Title: "Cardiac Cycle", Page: intPointer(2), IsNew: true},
	}

	response := serve(store, http.MethodGet, "/reviews/due")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	want := `{"cards":[{"card_id":"a","question":"Due review?","title":"Week 1","is_new":false},{"card_id":"b","question":"New card?","title":"Cardiac Cycle","page":2,"is_new":true}],"next_due_at":"2026-09-12T09:00:00Z"}` + "\n"
	if got := response.Body.String(); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
	if len(store.dueCalls) != 1 || store.dueCalls[0] != (dueCall{limit: defaultDueLimit, newCardCap: newCardsPerDay}) {
		t.Fatalf("due calls = %+v, want default limit and the daily new-card cap", store.dueCalls)
	}
}

func TestDueReviewsWithNothingDue(t *testing.T) {
	response := serve(newMemoryStore(), http.MethodGet, "/reviews/due")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	if got := response.Body.String(); got != `{"cards":[],"next_due_at":null}`+"\n" {
		t.Fatalf("body = %s", got)
	}
}

func TestDueReviewsLimit(t *testing.T) {
	tests := []struct {
		query     string
		status    int
		wantLimit int
	}{
		{query: "?limit=1", status: http.StatusOK, wantLimit: 1},
		{query: "?limit=100", status: http.StatusOK, wantLimit: 100},
		{query: "?limit=0", status: http.StatusBadRequest},
		{query: "?limit=101", status: http.StatusBadRequest},
		{query: "?limit=ten", status: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			store := newMemoryStore()
			response := serve(store, http.MethodGet, "/reviews/due"+tt.query)

			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.status, response.Body.String())
			}
			if tt.status == http.StatusOK && store.dueCalls[0].limit != tt.wantLimit {
				t.Fatalf("limit = %d, want %d", store.dueCalls[0].limit, tt.wantLimit)
			}
			if tt.status != http.StatusOK && len(store.dueCalls) != 0 {
				t.Fatal("store was queried for an invalid limit")
			}
		})
	}
}

func TestSubmitReviewGradesAndReschedules(t *testing.T) {
	store := newMemoryStore()
	card := testReviewCard()
	store.reviewCards[testCardUUID] = card
	generator := &fakeGenerator{grade: generation.Grade{Score: 4, Rationale: "Right idea, aortic side vague."}}

	before := time.Now()
	response := serveReview(store, generator, testCardUUID, `{"answer": "  pressure is in between  "}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}

	if len(generator.gradeCalls) != 1 {
		t.Fatalf("grade calls = %d, want 1", len(generator.gradeCalls))
	}
	call := generator.gradeCalls[0]
	if call.question != card.Question || call.expectedAnswer != card.ExpectedAnswer || call.passage != card.Passage || call.answer != "pressure is in between" {
		t.Fatalf("grade call = %+v", call)
	}

	if len(store.reviews) != 1 {
		t.Fatalf("recorded reviews = %d, want 1", len(store.reviews))
	}
	recorded := store.reviews[0]
	wantSchedule := scheduling.State{Repetitions: 1, IntervalDays: 1, EaseFactor: 2.5}
	if recorded.CardID != testCardUUID || recorded.Answer != "pressure is in between" || recorded.Grade.Score != 4 || recorded.Schedule != wantSchedule {
		t.Fatalf("recorded review = %+v", recorded)
	}
	if !recorded.PreviousUpdatedAt.Equal(card.UpdatedAt) {
		t.Fatalf("previous updated_at = %v, want %v", recorded.PreviousUpdatedAt, card.UpdatedAt)
	}
	if recorded.DueAt.Before(before.AddDate(0, 0, 1)) || recorded.DueAt.After(time.Now().AddDate(0, 0, 1)) {
		t.Fatalf("due at = %v, want one day from now", recorded.DueAt)
	}

	var body reviewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Score != 4 || body.Label != "Good" || body.Rationale != "Right idea, aortic side vague." || body.ExpectedAnswer != card.ExpectedAnswer {
		t.Fatalf("response = %+v", body)
	}
	if body.IntervalDays != 1 || !body.NextDueAt.Equal(recorded.DueAt) {
		t.Fatalf("interval = %d, next due = %v, want 1 and %v", body.IntervalDays, body.NextDueAt, recorded.DueAt)
	}
	if body.Source == nil {
		t.Fatal("source = nil, want the passage in context")
	}
	if body.Source.Title != "Cardiac Cycle" || body.Source.Page == nil || *body.Source.Page != 2 || body.Source.Passage != card.Passage || body.Source.After != " Pressure rises." {
		t.Fatalf("source = %+v", body.Source)
	}
}

func TestSubmitReviewOmitsSourceWithoutALocation(t *testing.T) {
	store := newMemoryStore()
	card := testReviewCard()
	card.Location.Start = nil
	card.Location.End = nil
	store.reviewCards[testCardUUID] = card
	generator := &fakeGenerator{grade: generation.Grade{Score: 5, Rationale: "Complete."}}

	response := serveReview(store, generator, testCardUUID, `{"answer": "in between"}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"source"`) {
		t.Fatalf("body = %s, want no source for a chunk without a stored location", response.Body.String())
	}
}

func TestSubmitReviewRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name   string
		cardID string
		body   string
		status int
	}{
		{name: "invalid card id", cardID: "not-a-uuid", body: `{"answer": "x"}`, status: http.StatusBadRequest},
		{name: "invalid JSON", cardID: testCardUUID, body: `{"answer":`, status: http.StatusBadRequest},
		{name: "unknown field", cardID: testCardUUID, body: `{"answer": "x", "score": 5}`, status: http.StatusBadRequest},
		{name: "two objects", cardID: testCardUUID, body: `{"answer": "x"}{"answer": "y"}`, status: http.StatusBadRequest},
		{name: "empty answer", cardID: testCardUUID, body: `{"answer": ""}`, status: http.StatusBadRequest},
		{name: "blank answer", cardID: testCardUUID, body: `{"answer": " \n\t "}`, status: http.StatusBadRequest},
		{name: "answer over the limit", cardID: testCardUUID, body: `{"answer": "` + strings.Repeat("é", maxReviewAnswerCharacters+1) + `"}`, status: http.StatusBadRequest},
		{name: "unknown card", cardID: "00000000-0000-0000-0000-000000000000", body: `{"answer": "x"}`, status: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			store.reviewCards[testCardUUID] = testReviewCard()
			generator := &fakeGenerator{grade: generation.Grade{Score: 5, Rationale: "x"}}

			response := serveReview(store, generator, tt.cardID, tt.body)

			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.status, response.Body.String())
			}
			if len(generator.gradeCalls) != 0 || len(store.reviews) != 0 {
				t.Fatalf("grade calls = %d, reviews = %d, want none", len(generator.gradeCalls), len(store.reviews))
			}
		})
	}
}

func TestSubmitReviewAcceptsAnAnswerAtTheLimit(t *testing.T) {
	store := newMemoryStore()
	store.reviewCards[testCardUUID] = testReviewCard()
	generator := &fakeGenerator{grade: generation.Grade{Score: 3, Rationale: "x"}}

	response := serveReview(store, generator, testCardUUID, `{"answer": "`+strings.Repeat("é", maxReviewAnswerCharacters)+`"}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
}

func TestSubmitReviewWritesNothingWhenGradingFails(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "provider failure", err: &generation.ProviderError{StatusCode: http.StatusServiceUnavailable}},
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "invalid grade", err: generation.ErrInvalidGrade},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			store.reviewCards[testCardUUID] = testReviewCard()
			generator := &fakeGenerator{gradeErr: tt.err}

			response := serveReview(store, generator, testCardUUID, `{"answer": "in between"}`)

			if response.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusBadGateway, response.Body.String())
			}
			if len(store.reviews) != 0 {
				t.Fatalf("recorded reviews = %d, want none", len(store.reviews))
			}
		})
	}
}

func TestSubmitReviewReportsAConcurrentSubmission(t *testing.T) {
	store := newMemoryStore()
	store.reviewCards[testCardUUID] = testReviewCard()
	store.reviewErr = errReviewConflict
	generator := &fakeGenerator{grade: generation.Grade{Score: 5, Rationale: "x"}}

	response := serveReview(store, generator, testCardUUID, `{"answer": "in between"}`)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body.String())
	}
}

func TestSubmitReviewReportsAStoreFailure(t *testing.T) {
	store := newMemoryStore()
	store.reviewCards[testCardUUID] = testReviewCard()
	store.reviewErr = errors.New("database unavailable")
	generator := &fakeGenerator{grade: generation.Grade{Score: 5, Rationale: "x"}}

	response := serveReview(store, generator, testCardUUID, `{"answer": "in between"}`)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusInternalServerError, response.Body.String())
	}
}

func TestGradeLabel(t *testing.T) {
	want := []string{"Forgot", "Forgot", "Almost", "Hard", "Good", "Easy"}
	for score, label := range want {
		if got := gradeLabel(score); got != label {
			t.Errorf("gradeLabel(%d) = %q, want %q", score, got, label)
		}
	}
}

func TestSubmitReviewOnADemoAccountGradesWithoutRecording(t *testing.T) {
	store := newMemoryStore()
	store.reviewCards[testCardUUID] = testReviewCard()
	generator := &fakeGenerator{grade: generation.Grade{Score: 4, Rationale: "Right idea."}}

	request := httptest.NewRequest(http.MethodPost, "/reviews/"+testCardUUID, strings.NewReader(`{"answer": "pressure is in between"}`))
	signIn(store, request)
	store.users["signed-in@example.com"] = user{ID: testAccountID, Email: "signed-in@example.com", IsDemo: true}
	response := httptest.NewRecorder()
	NewHandler(store, &fakeEmbedder{}, generator, &fakePublisher{}, &fakeExtractor{}).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
	}
	if len(generator.gradeCalls) != 1 {
		t.Fatalf("grade calls = %d, want 1", len(generator.gradeCalls))
	}
	if len(store.reviews) != 0 {
		t.Fatalf("recorded reviews = %d, want 0 for a demo account", len(store.reviews))
	}

	var body reviewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Score != 4 || body.Source == nil {
		t.Fatalf("response = %+v, want the grade and the source passage", body)
	}
}
