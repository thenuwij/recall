package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingStore struct {
	fakeIngestionStore

	concurrent atomic.Int64
	peak       atomic.Int64
	release    chan struct{}
	claimed    atomic.Int64
	limit      int64
}

func (s *blockingStore) claimIngestionJob(_ context.Context, _ time.Duration) (ingestionJob, error) {
	if s.claimed.Add(1) > s.limit {
		return ingestionJob{}, errNoIngestionJob
	}

	return ingestionJob{ID: "job", DocumentID: "document", Kind: jobKindEmbed, Attempts: 1}, nil
}

func (s *blockingStore) chunksAwaitingEmbedding(_ context.Context, _ string) ([]pendingChunk, error) {
	running := s.concurrent.Add(1)
	for {
		peak := s.peak.Load()
		if running <= peak || s.peak.CompareAndSwap(peak, running) {
			break
		}
	}

	<-s.release
	s.concurrent.Add(-1)

	return nil, nil
}

func (s *blockingStore) completeIngestionJob(_ context.Context, _ string, _ int, _ []embeddedChunk, _ string) error {
	return nil
}

func TestRunPoolProcessesJobsConcurrently(t *testing.T) {
	store := &blockingStore{release: make(chan struct{}), limit: 3}
	worker := &Worker{store: store, embedder: &fakeEmbedder{}, lease: time.Minute, poll: time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		_ = worker.RunPool(ctx, 3)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for store.peak.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	peak := store.peak.Load()
	close(store.release)
	cancel()
	group.Wait()

	if peak < 2 {
		t.Fatalf("peak concurrent jobs = %d, want at least 2: the pool is not running jobs in parallel", peak)
	}
}

func TestRunPoolStopsPromptlyWhenCancelled(t *testing.T) {
	store := &fakeIngestionStore{claimErr: errNoIngestionJob}
	worker := &Worker{store: store, embedder: &fakeEmbedder{}, lease: time.Minute, poll: 50 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = worker.RunPool(ctx, 4)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPool did not stop within 2s of cancellation")
	}
}

func TestProcessOneRenewsTheLeaseWhileWorking(t *testing.T) {
	store := &fakeIngestionStore{
		job:     ingestionJob{ID: "job-1", DocumentID: "document-1", Kind: jobKindEmbed, Attempts: 1},
		pending: []pendingChunk{{ID: "chunk-a", Content: "first"}},
	}
	worker := &Worker{store: store, embedder: &slowEmbedder{delay: 120 * time.Millisecond}, lease: 90 * time.Millisecond, poll: time.Millisecond}

	if _, err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}

	if store.renewalCount() == 0 {
		t.Fatal("lease was never renewed during a job that outlived a third of its lease")
	}
}

type slowEmbedder struct {
	delay time.Duration
}

func (e *slowEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	time.Sleep(e.delay)

	result := make([][]float32, len(inputs))
	for index := range inputs {
		result[index] = make([]float32, 1536)
		result[index][0] = float32(index + 1)
	}

	return result, nil
}
