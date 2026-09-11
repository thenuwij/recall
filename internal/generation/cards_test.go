package generation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

var testCardPassages = []Passage{
	{Marker: 1, Content: "The sinoatrial node sets the resting heart rate."},
	{Marker: 2, Content: "Both valves stay closed during isovolumetric contraction."},
}

func TestGenerateCardsSendsExpectedRequest(t *testing.T) {
	var received chatRequest
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		respondWith(t, w, `{"cards": [{"marker": 2, "question": "Why do both valves stay closed?", "expected_answer": "Pressure is between atrial and aortic."}]}`)
	})

	cards, err := client.GenerateCards(context.Background(), testCardPassages)
	if err != nil {
		t.Fatalf("GenerateCards: %v", err)
	}

	want := []GeneratedCard{{Marker: 2, Question: "Why do both valves stay closed?", ExpectedAnswer: "Pressure is between atrial and aortic."}}
	if !reflect.DeepEqual(cards, want) {
		t.Fatalf("cards = %+v, want %+v", cards, want)
	}
	if received.Model != CardModel || received.Temperature != 0 || received.MaxCompletionTokens != maxCardTokens {
		t.Fatalf("request settings = %s, %v, %d", received.Model, received.Temperature, received.MaxCompletionTokens)
	}
	if received.ResponseFormat == nil || received.ResponseFormat.Type != "json_object" {
		t.Fatalf("response format = %+v, want json_object", received.ResponseFormat)
	}
	if len(received.Messages) != 2 || received.Messages[0].Role != "system" || received.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v, want system then user", received.Messages)
	}
	for _, part := range []string{"[1] " + testCardPassages[0].Content, "[2] " + testCardPassages[1].Content} {
		if !strings.Contains(received.Messages[1].Content, part) {
			t.Errorf("user message does not contain %q", part)
		}
	}
}

func TestGenerateCardsDropsInvalidCards(t *testing.T) {
	reply := `{"cards": [
		{"marker": 1, "question": "What sets the resting heart rate?", "expected_answer": "The sinoatrial node."},
		{"marker": 3, "question": "Cites a passage it was not given?", "expected_answer": "Yes."},
		{"marker": 0, "question": "Marker zero?", "expected_answer": "Yes."},
		{"marker": 2, "question": "  ", "expected_answer": "Blank question."},
		{"marker": 2, "question": "Blank answer?", "expected_answer": ""},
		{"marker": 1, "question": "Second card for passage one?", "expected_answer": "Allowed."},
		{"marker": 1, "question": "Third card for passage one?", "expected_answer": "Over the limit."}
	]}`
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		respondWith(t, w, reply)
	})

	cards, err := client.GenerateCards(context.Background(), testCardPassages)
	if err != nil {
		t.Fatalf("GenerateCards: %v", err)
	}

	want := []GeneratedCard{
		{Marker: 1, Question: "What sets the resting heart rate?", ExpectedAnswer: "The sinoatrial node."},
		{Marker: 1, Question: "Second card for passage one?", ExpectedAnswer: "Allowed."},
	}
	if !reflect.DeepEqual(cards, want) {
		t.Fatalf("cards = %+v, want %+v", cards, want)
	}
}

func TestGenerateCardsAcceptsNoCards(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		respondWith(t, w, `{"cards": []}`)
	})

	cards, err := client.GenerateCards(context.Background(), testCardPassages)
	if err != nil {
		t.Fatalf("GenerateCards: %v", err)
	}
	if len(cards) != 0 {
		t.Fatalf("cards = %+v, want none", cards)
	}
}

func TestGenerateCardsRejectsMalformedReplies(t *testing.T) {
	tests := []struct {
		name  string
		reply string
	}{
		{name: "prose instead of JSON", reply: `Here are some questions about the heart.`},
		{name: "missing cards", reply: `{"questions": []}`},
		{name: "null cards", reply: `{"cards": null}`},
		{name: "marker as string", reply: `{"cards": [{"marker": "1", "question": "Q?", "expected_answer": "A."}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				respondWith(t, w, tt.reply)
			})

			_, err := client.GenerateCards(context.Background(), testCardPassages)
			if !errors.Is(err, ErrInvalidCards) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidCards)
			}
		})
	}
}

func TestGenerateCardsRejectsNoPassagesWithoutCallingProvider(t *testing.T) {
	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		respondWith(t, w, `{"cards": []}`)
	})

	if _, err := client.GenerateCards(context.Background(), nil); err == nil {
		t.Fatal("GenerateCards() error = nil, want an error for no passages")
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
}

func TestGenerateCardsReturnsProviderError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	})

	_, err := client.GenerateCards(context.Background(), testCardPassages)
	var providerError *ProviderError
	if !errors.As(err, &providerError) {
		t.Fatalf("error = %v, want a *ProviderError", err)
	}
	if providerError.Retryable() {
		t.Fatal("retryable = true, want false for a 400")
	}
}
