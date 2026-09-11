package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/thenujawijesuriya/recall/internal/generation"
)

type fakeCardGenerator struct {
	replies [][]generation.GeneratedCard
	err     error
	calls   [][]generation.Passage
}

func (g *fakeCardGenerator) GenerateCards(_ context.Context, passages []generation.Passage) ([]generation.GeneratedCard, error) {
	g.calls = append(g.calls, passages)
	if g.err != nil {
		return nil, g.err
	}
	if len(g.calls) > len(g.replies) {
		return nil, nil
	}
	return g.replies[len(g.calls)-1], nil
}

type questionEmbedder struct {
	vectors map[string][]float32
	err     error
	calls   int
}

func (e *questionEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}

	result := make([][]float32, len(inputs))
	for index, input := range inputs {
		result[index] = e.vectors[input]
	}
	return result, nil
}

func newCardWorker(store *fakeIngestionStore, generator *fakeCardGenerator, embedder embeddingGenerator) *Worker {
	worker := newTestWorker(store, embedder)
	worker.generator = generator
	return worker
}

func cardJobStore(chunkCount int) *fakeIngestionStore {
	chunks := make([]pendingChunk, chunkCount)
	for index := range chunks {
		chunks[index] = pendingChunk{ID: fmt.Sprintf("chunk-%d", index), Content: fmt.Sprintf("content %d", index)}
	}
	return &fakeIngestionStore{
		job:    ingestionJob{ID: "job-2", DocumentID: "document-1", Kind: jobKindCards, Attempts: 1},
		chunks: chunks,
	}
}

func TestWorkerGeneratesCardsInBatchesOfFive(t *testing.T) {
	store := cardJobStore(7)
	generator := &fakeCardGenerator{replies: [][]generation.GeneratedCard{
		{{Marker: 1, Question: "q0", ExpectedAnswer: "a0"}, {Marker: 5, Question: "q4", ExpectedAnswer: "a4"}},
		{{Marker: 2, Question: "q6", ExpectedAnswer: "a6"}},
	}}
	embedder := &questionEmbedder{vectors: map[string][]float32{
		"q0": {1, 0, 0},
		"q4": {0, 1, 0},
		"q6": {0, 0, 1},
	}}

	worked, err := newCardWorker(store, generator, embedder).ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}
	if !worked {
		t.Fatal("worked = false, want true when a job was claimed")
	}

	if len(generator.calls) != 2 || len(generator.calls[0]) != 5 || len(generator.calls[1]) != 2 {
		t.Fatalf("batch sizes = %v, want 5 then 2", generator.calls)
	}
	secondBatch := generator.calls[1]
	if secondBatch[0].Marker != 1 || secondBatch[0].Content != "content 5" || secondBatch[1].Marker != 2 {
		t.Fatalf("second batch = %+v, want markers restarting at 1 from chunk 5", secondBatch)
	}

	want := []newCard{
		{ChunkID: "chunk-0", Question: "q0", ExpectedAnswer: "a0"},
		{ChunkID: "chunk-4", Question: "q4", ExpectedAnswer: "a4"},
		{ChunkID: "chunk-6", Question: "q6", ExpectedAnswer: "a6"},
	}
	if !reflect.DeepEqual(store.cards, want) {
		t.Fatalf("cards = %+v, want %+v", store.cards, want)
	}
	if store.completeID != "job-2" {
		t.Errorf("completed job = %q, want %q", store.completeID, "job-2")
	}
	if embedder.calls != 1 {
		t.Errorf("embedder calls = %d, want one call for all questions", embedder.calls)
	}
}

func TestWorkerDropsNearDuplicateQuestions(t *testing.T) {
	store := cardJobStore(2)
	generator := &fakeCardGenerator{replies: [][]generation.GeneratedCard{{
		{Marker: 1, Question: "original", ExpectedAnswer: "a"},
		{Marker: 2, Question: "near duplicate", ExpectedAnswer: "a"},
		{Marker: 2, Question: "related but distinct", ExpectedAnswer: "b"},
	}}}
	embedder := &questionEmbedder{vectors: map[string][]float32{
		"original":             {1, 0},
		"near duplicate":       {1, 0.3},
		"related but distinct": {1, 0.5},
	}}

	if _, err := newCardWorker(store, generator, embedder).ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}

	want := []newCard{
		{ChunkID: "chunk-0", Question: "original", ExpectedAnswer: "a"},
		{ChunkID: "chunk-1", Question: "related but distinct", ExpectedAnswer: "b"},
	}
	if !reflect.DeepEqual(store.cards, want) {
		t.Fatalf("cards = %+v, want %+v", store.cards, want)
	}
}

func TestWorkerCompletesACardJobWithNoCards(t *testing.T) {
	tests := []struct {
		name   string
		chunks int
	}{
		{name: "nothing testable", chunks: 1},
		{name: "no chunks left", chunks: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := cardJobStore(tt.chunks)
			generator := &fakeCardGenerator{}
			embedder := &questionEmbedder{}

			if _, err := newCardWorker(store, generator, embedder).ProcessOne(context.Background()); err != nil {
				t.Fatalf("ProcessOne: %v", err)
			}
			if store.completeID != "job-2" || len(store.cards) != 0 {
				t.Fatalf("completed job = %q with %d cards, want job-2 with none", store.completeID, len(store.cards))
			}
			if len(generator.calls) != tt.chunks {
				t.Errorf("generator calls = %d, want %d", len(generator.calls), tt.chunks)
			}
			if embedder.calls != 0 {
				t.Errorf("embedder calls = %d, want 0 with no questions to compare", embedder.calls)
			}
		})
	}
}

func TestWorkerClassifiesCardJobFailures(t *testing.T) {
	tests := []struct {
		name         string
		generatorErr error
		embedderErr  error
		permanent    bool
	}{
		{name: "malformed model output", generatorErr: generation.ErrInvalidCards, permanent: false},
		{name: "rate limited", generatorErr: &generation.ProviderError{StatusCode: http.StatusTooManyRequests}, permanent: false},
		{name: "rejected request", generatorErr: &generation.ProviderError{StatusCode: http.StatusBadRequest}, permanent: true},
		{name: "question embedding fails", embedderErr: errors.New("provider unavailable"), permanent: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := cardJobStore(2)
			generator := &fakeCardGenerator{err: tt.generatorErr, replies: [][]generation.GeneratedCard{{
				{Marker: 1, Question: "q0", ExpectedAnswer: "a0"},
				{Marker: 2, Question: "q1", ExpectedAnswer: "a1"},
			}}}
			embedder := &questionEmbedder{err: tt.embedderErr}

			if _, err := newCardWorker(store, generator, embedder).ProcessOne(context.Background()); err == nil {
				t.Fatal("ProcessOne() error = nil, want the failure reported")
			}
			if len(store.failures) != 1 {
				t.Fatalf("failures = %d, want 1", len(store.failures))
			}
			if store.failures[0].permanent != tt.permanent {
				t.Errorf("permanent = %t, want %t", store.failures[0].permanent, tt.permanent)
			}
			if store.completeID != "" || store.cards != nil {
				t.Errorf("job completed with %d cards despite the failure", len(store.cards))
			}
		})
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float64
	}{
		{name: "same direction", a: []float32{2, 0}, b: []float32{5, 0}, want: 1},
		{name: "orthogonal", a: []float32{1, 0}, b: []float32{0, 1}, want: 0},
		{name: "opposite", a: []float32{1, 0}, b: []float32{-1, 0}, want: -1},
		{name: "zero vector", a: []float32{0, 0}, b: []float32{1, 0}, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cosineSimilarity(tt.a, tt.b); got != tt.want {
				t.Fatalf("cosineSimilarity = %v, want %v", got, tt.want)
			}
		})
	}
}
