package api

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thenujawijesuriya/recall/internal/embedding"
	"github.com/thenujawijesuriya/recall/internal/queue"
)

type fakeIngestionStore struct {
	job         ingestionJob
	claimErr    error
	pending     []pendingChunk
	pendingErr  error
	completeErr error
	chunks      []pendingChunk

	claims     int
	completed  []embeddedChunk
	completeID string
	cards      []newCard
	failures   []recordedFailure
}

type recordedFailure struct {
	reason    string
	permanent bool
	backoff   time.Duration
}

func (s *fakeIngestionStore) claimIngestionJob(_ context.Context, _ time.Duration) (ingestionJob, error) {
	s.claims++
	if s.claimErr != nil {
		return ingestionJob{}, s.claimErr
	}
	return s.job, nil
}

func (s *fakeIngestionStore) chunksAwaitingEmbedding(_ context.Context, _ string) ([]pendingChunk, error) {
	if s.pendingErr != nil {
		return nil, s.pendingErr
	}
	return s.pending, nil
}

func (s *fakeIngestionStore) completeIngestionJob(_ context.Context, jobID string, embedded []embeddedChunk, _ string) error {
	if s.completeErr != nil {
		return s.completeErr
	}
	s.completeID = jobID
	s.completed = embedded
	return nil
}

func (s *fakeIngestionStore) failIngestionJob(_ context.Context, _ ingestionJob, reason string, permanent bool, backoff time.Duration) error {
	s.failures = append(s.failures, recordedFailure{reason: reason, permanent: permanent, backoff: backoff})
	return nil
}

func (s *fakeIngestionStore) documentChunks(_ context.Context, _ string) ([]pendingChunk, error) {
	return s.chunks, nil
}

func (s *fakeIngestionStore) completeCardJob(_ context.Context, jobID string, cards []newCard) error {
	if s.completeErr != nil {
		return s.completeErr
	}
	s.completeID = jobID
	s.cards = cards
	return nil
}

func newTestWorker(store ingestionStore, embedder embeddingGenerator) *Worker {
	return &Worker{store: store, embedder: embedder, lease: time.Minute, poll: time.Millisecond}
}

func TestWorkerEmbedsPendingChunksAndCompletesTheJob(t *testing.T) {
	store := &fakeIngestionStore{
		job:     ingestionJob{ID: "job-1", DocumentID: "document-1", Attempts: 1},
		pending: []pendingChunk{{ID: "chunk-a", Content: "first"}, {ID: "chunk-b", Content: "second"}},
	}

	worked, err := newTestWorker(store, &fakeEmbedder{}).ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}
	if !worked {
		t.Fatal("worked = false, want true when a job was claimed")
	}
	if store.completeID != "job-1" {
		t.Errorf("completed job = %q, want %q", store.completeID, "job-1")
	}
	if len(store.completed) != 2 {
		t.Fatalf("completed chunks = %d, want 2", len(store.completed))
	}
	if store.completed[0].ID != "chunk-a" || store.completed[1].ID != "chunk-b" {
		t.Error("vectors were not matched back to the chunks they belong to")
	}
	if len(store.completed[0].Embedding) != embedding.Dimensions {
		t.Errorf("vector length = %d, want %d", len(store.completed[0].Embedding), embedding.Dimensions)
	}
	if len(store.failures) != 0 {
		t.Errorf("failures = %v, want none", store.failures)
	}
}

func TestWorkerReportsNoWorkWithoutCallingTheProvider(t *testing.T) {
	store := &fakeIngestionStore{claimErr: errNoIngestionJob}
	embedder := &fakeEmbedder{}

	worked, err := newTestWorker(store, embedder).ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}
	if worked {
		t.Error("worked = true, want false for an empty queue")
	}
	if embedder.calls != 0 {
		t.Errorf("embedder calls = %d, want 0: an empty queue must cost nothing", embedder.calls)
	}
}

func TestWorkerCompletesAJobWithNothingLeftToEmbed(t *testing.T) {
	store := &fakeIngestionStore{job: ingestionJob{ID: "job-1", DocumentID: "document-1"}}
	embedder := &fakeEmbedder{}

	if _, err := newTestWorker(store, embedder).ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}
	if store.completeID != "job-1" {
		t.Errorf("completed job = %q, want the job to be completed", store.completeID)
	}
	if embedder.calls != 0 {
		t.Errorf("embedder calls = %d, want 0", embedder.calls)
	}
}

func TestWorkerTreatsProviderFailureAsRetryable(t *testing.T) {
	store := &fakeIngestionStore{
		job:     ingestionJob{ID: "job-1", DocumentID: "document-1", Attempts: 1},
		pending: []pendingChunk{{ID: "chunk-a", Content: "first"}},
	}
	embedder := &fakeEmbedder{err: errors.New("provider unavailable")}

	if _, err := newTestWorker(store, embedder).ProcessOne(context.Background()); err == nil {
		t.Fatal("ProcessOne() error = nil, want the provider failure reported")
	}
	if len(store.failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(store.failures))
	}
	if store.failures[0].permanent {
		t.Error("permanent = true, want false: a provider outage may succeed on retry")
	}
	if store.failures[0].backoff == 0 {
		t.Error("backoff = 0, want a delay before retrying a failed provider")
	}
	if store.completed != nil {
		t.Error("chunks were completed despite the provider failing")
	}
}

func TestWorkerTreatsWrongVectorDimensionAsPermanent(t *testing.T) {
	store := &fakeIngestionStore{
		job:     ingestionJob{ID: "job-1", DocumentID: "document-1", Attempts: 1},
		pending: []pendingChunk{{ID: "chunk-a", Content: "first"}},
	}
	embedder := shortVectorEmbedder{}

	if _, err := newTestWorker(store, embedder).ProcessOne(context.Background()); err == nil {
		t.Fatal("ProcessOne() error = nil, want a wrong-dimension vector rejected")
	}
	if len(store.failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(store.failures))
	}
	if !store.failures[0].permanent {
		t.Error("permanent = false, want true: a malformed vector fails identically on retry")
	}
}

func TestWorkerTreatsWrongVectorCountAsPermanent(t *testing.T) {
	store := &fakeIngestionStore{
		job:     ingestionJob{ID: "job-1", DocumentID: "document-1", Attempts: 1},
		pending: []pendingChunk{{ID: "chunk-a", Content: "first"}, {ID: "chunk-b", Content: "second"}},
	}
	embedder := &oneVectorEmbedder{}

	if _, err := newTestWorker(store, embedder).ProcessOne(context.Background()); err == nil {
		t.Fatal("ProcessOne() error = nil, want a short vector set rejected")
	}
	if len(store.failures) != 1 || !store.failures[0].permanent {
		t.Errorf("failures = %v, want one permanent failure", store.failures)
	}
}

func TestWorkerDoesNotFailAJobWhenTheContextIsCancelled(t *testing.T) {
	store := &fakeIngestionStore{
		job:     ingestionJob{ID: "job-1", DocumentID: "document-1", Attempts: 1},
		pending: []pendingChunk{{ID: "chunk-a", Content: "first"}},
	}
	embedder := &fakeEmbedder{err: context.Canceled}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := newTestWorker(store, embedder).ProcessOne(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessOne() error = %v, want context.Canceled", err)
	}
	if len(store.failures) != 0 {
		t.Errorf("failures = %v, want none: shutdown is not a job failure", store.failures)
	}
}

func TestWorkerRunStopsOnContextCancellation(t *testing.T) {
	store := &fakeIngestionStore{claimErr: errNoIngestionJob}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := newTestWorker(store, &fakeEmbedder{}).Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want context.DeadlineExceeded", err)
	}
	if store.claims == 0 {
		t.Error("claims = 0, want the loop to have polled at least once")
	}
}

type oneVectorEmbedder struct{}

func (e *oneVectorEmbedder) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return [][]float32{make([]float32, embedding.Dimensions)}, nil
}

func TestWorkerLogsJobLifecycle(t *testing.T) {
	var buffer bytes.Buffer
	store := &fakeIngestionStore{
		job:     ingestionJob{ID: "job-1", DocumentID: "document-1", Kind: jobKindEmbed, Attempts: 1},
		pending: []pendingChunk{{ID: "chunk-a", Content: "first"}},
	}
	worker := newTestWorker(store, &fakeEmbedder{})
	worker.logger = log.New(&buffer, "", 0)

	if _, err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}

	output := buffer.String()
	for _, want := range []string{
		"claimed job=job-1 kind=embed document=document-1 attempt=1",
		"embedding job=job-1 chunks=1",
		"completed job=job-1 document=document-1 chunks=1",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("log missing %q; got:\n%s", want, output)
		}
	}
}

func TestWorkerLogsFailureWithItsClassification(t *testing.T) {
	var buffer bytes.Buffer
	store := &fakeIngestionStore{
		job:     ingestionJob{ID: "job-1", DocumentID: "document-1", Attempts: 1},
		pending: []pendingChunk{{ID: "chunk-a", Content: "first"}},
	}
	worker := newTestWorker(store, &fakeEmbedder{err: errors.New("provider unavailable")})
	worker.logger = log.New(&buffer, "", 0)

	if _, err := worker.ProcessOne(context.Background()); err == nil {
		t.Fatal("ProcessOne() error = nil, want the provider failure reported")
	}

	output := buffer.String()
	for _, want := range []string{"failed job=job-1", "permanent=false", "terminal=false", "provider unavailable"} {
		if !strings.Contains(output, want) {
			t.Errorf("log missing %q; got:\n%s", want, output)
		}
	}
}

type fakeNotifier struct {
	batches [][]queue.Message
	err     error

	received int
	acked    []string
}

func (n *fakeNotifier) Receive(_ context.Context, _ int64, _ time.Duration) ([]queue.Message, error) {
	if n.err != nil {
		return nil, n.err
	}
	if n.received >= len(n.batches) {
		return nil, nil
	}
	batch := n.batches[n.received]
	n.received++
	return batch, nil
}

func (n *fakeNotifier) Ack(_ context.Context, ids ...string) error {
	n.acked = append(n.acked, ids...)
	return nil
}

func TestWorkerProcessesAndAcknowledgesANotification(t *testing.T) {
	store := &fakeIngestionStore{
		job:     ingestionJob{ID: "job-1", DocumentID: "document-1", Attempts: 1},
		pending: []pendingChunk{{ID: "chunk-a", Content: "first"}},
	}
	notifier := &fakeNotifier{batches: [][]queue.Message{{{ID: "1-0", JobID: "job-1"}}}}
	worker := newTestWorker(store, &fakeEmbedder{})
	worker.notifier = notifier

	if err := worker.waitForWork(context.Background()); err != nil {
		t.Fatalf("waitForWork: %v", err)
	}

	if store.completeID != "job-1" {
		t.Errorf("completed job = %q, want the notified job processed", store.completeID)
	}
	if len(notifier.acked) != 1 || notifier.acked[0] != "1-0" {
		t.Errorf("acked = %v, want the message acknowledged after the work committed", notifier.acked)
	}
}

func TestWorkerDoesNotAcknowledgeWhenTheJobFails(t *testing.T) {
	store := &fakeIngestionStore{
		job:     ingestionJob{ID: "job-1", DocumentID: "document-1", Attempts: 1},
		pending: []pendingChunk{{ID: "chunk-a", Content: "first"}},
	}
	notifier := &fakeNotifier{batches: [][]queue.Message{{{ID: "1-0", JobID: "job-1"}}}}
	worker := newTestWorker(store, &fakeEmbedder{err: errors.New("provider unavailable")})
	worker.notifier = notifier

	if err := worker.waitForWork(context.Background()); err != nil {
		t.Fatalf("waitForWork: %v", err)
	}

	if len(notifier.acked) != 0 {
		t.Errorf("acked = %v, want no acknowledgement for work that did not succeed", notifier.acked)
	}
}

func TestWorkerAcknowledgesANotificationWithNoWorkLeft(t *testing.T) {
	store := &fakeIngestionStore{claimErr: errNoIngestionJob}
	notifier := &fakeNotifier{batches: [][]queue.Message{{{ID: "1-0", JobID: "job-1"}}}}
	worker := newTestWorker(store, &fakeEmbedder{})
	worker.notifier = notifier

	if err := worker.waitForWork(context.Background()); err != nil {
		t.Fatalf("waitForWork: %v", err)
	}

	if len(notifier.acked) != 1 {
		t.Errorf("acked = %v, want a duplicate notification acknowledged rather than redelivered forever", notifier.acked)
	}
}

func TestWorkerTreatsARateLimitAsRetryable(t *testing.T) {
	store := &fakeIngestionStore{
		job:     ingestionJob{ID: "job-1", DocumentID: "document-1", Attempts: 1},
		pending: []pendingChunk{{ID: "chunk-a", Content: "first"}},
	}
	embedder := &fakeEmbedder{err: &embedding.ProviderError{StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests"}}

	if _, err := newTestWorker(store, embedder).ProcessOne(context.Background()); err == nil {
		t.Fatal("ProcessOne() error = nil, want the rate limit reported")
	}
	if len(store.failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(store.failures))
	}
	if store.failures[0].permanent {
		t.Error("permanent = true, want false: a rate limit succeeds once the window resets")
	}
}

func TestWorkerTreatsARejectedRequestAsPermanent(t *testing.T) {
	store := &fakeIngestionStore{
		job:     ingestionJob{ID: "job-1", DocumentID: "document-1", Attempts: 1},
		pending: []pendingChunk{{ID: "chunk-a", Content: "first"}},
	}
	embedder := &fakeEmbedder{err: &embedding.ProviderError{StatusCode: http.StatusBadRequest, Status: "400 Bad Request"}}

	if _, err := newTestWorker(store, embedder).ProcessOne(context.Background()); err == nil {
		t.Fatal("ProcessOne() error = nil, want the rejection reported")
	}
	if len(store.failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(store.failures))
	}
	if !store.failures[0].permanent {
		t.Error("permanent = false, want true: a rejected request fails identically on retry")
	}
}
