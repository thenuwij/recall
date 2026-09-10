package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/thenujawijesuriya/recall/internal/embedding"
)

const (
	defaultJobLease    = 2 * time.Minute
	defaultPollTimeout = 2 * time.Second
)

type ingestionStore interface {
	claimIngestionJob(ctx context.Context, lease time.Duration) (ingestionJob, error)
	chunksAwaitingEmbedding(ctx context.Context, documentID string) ([]pendingChunk, error)
	completeIngestionJob(ctx context.Context, jobID string, embedded []embeddedChunk, model string) error
	failIngestionJob(ctx context.Context, job ingestionJob, reason string, permanent bool, backoff time.Duration) error
}

type Worker struct {
	store    ingestionStore
	embedder embeddingGenerator
	lease    time.Duration
	poll     time.Duration
	logger   *log.Logger
}

func NewWorker(store *PostgresStore, embedder embeddingGenerator) *Worker {
	return &Worker{
		store:    store,
		embedder: embedder,
		lease:    defaultJobLease,
		poll:     defaultPollTimeout,
		logger:   log.Default(),
	}
}

func (w *Worker) logf(format string, args ...any) {
	if w.logger == nil {
		return
	}

	w.logger.Printf("ingestion: "+format, args...)
}

func (w *Worker) Run(ctx context.Context) error {
	for {
		worked, err := w.ProcessOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.logf("%v", err)
		}

		if worked && ctx.Err() == nil {
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.poll):
		}
	}
}

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	job, err := w.store.claimIngestionJob(ctx, w.lease)
	if errors.Is(err, errNoIngestionJob) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim job: %w", err)
	}

	started := time.Now()
	w.logf("claimed job=%s document=%s attempt=%d", job.ID, job.DocumentID, job.Attempts)

	pending, err := w.store.chunksAwaitingEmbedding(ctx, job.DocumentID)
	if err != nil {
		return true, w.fail(ctx, job, fmt.Sprintf("read pending chunks: %v", err), false)
	}

	if len(pending) == 0 {
		w.logf("nothing to embed job=%s document=%s", job.ID, job.DocumentID)
		return true, w.store.completeIngestionJob(ctx, job.ID, nil, embedding.Model)
	}

	w.logf("embedding job=%s chunks=%d", job.ID, len(pending))

	inputs := make([]string, len(pending))
	for index, chunk := range pending {
		inputs[index] = chunk.Content
	}

	vectors, err := w.embedder.Embed(ctx, inputs)
	if err != nil {
		return true, w.fail(ctx, job, fmt.Sprintf("embed chunks: %v", err), false)
	}

	if len(vectors) != len(pending) {
		return true, w.fail(ctx, job, fmt.Sprintf("provider returned %d vectors for %d chunks", len(vectors), len(pending)), true)
	}
	embedded := make([]embeddedChunk, len(pending))
	for index, vector := range vectors {
		if len(vector) != embedding.Dimensions {
			return true, w.fail(ctx, job, fmt.Sprintf("provider returned a %d-dimension vector, want %d", len(vector), embedding.Dimensions), true)
		}
		embedded[index] = embeddedChunk{ID: pending[index].ID, Embedding: vector}
	}

	if err := w.store.completeIngestionJob(ctx, job.ID, embedded, embedding.Model); err != nil {
		return true, w.fail(ctx, job, fmt.Sprintf("store embeddings: %v", err), false)
	}

	w.logf("completed job=%s document=%s chunks=%d duration=%s", job.ID, job.DocumentID, len(embedded), time.Since(started).Round(time.Millisecond))
	return true, nil
}

func (w *Worker) fail(ctx context.Context, job ingestionJob, reason string, permanent bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	backoff := retryBackoff(job.Attempts)
	terminal := permanent || job.Attempts >= maxIngestionAttempts
	w.logf("failed job=%s document=%s attempt=%d permanent=%t terminal=%t backoff=%s reason=%q",
		job.ID, job.DocumentID, job.Attempts, permanent, terminal, backoff, reason)

	if err := w.store.failIngestionJob(ctx, job, reason, permanent, backoff); err != nil {
		return fmt.Errorf("record job failure: %w", err)
	}

	return errors.New(reason)
}

func retryBackoff(attempts int) time.Duration {
	if attempts <= 1 {
		return 5 * time.Second
	}

	return 30 * time.Second
}
