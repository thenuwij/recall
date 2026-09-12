package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/thenujawijesuriya/recall/internal/embedding"
	"github.com/thenujawijesuriya/recall/internal/generation"
	"github.com/thenujawijesuriya/recall/internal/queue"
)

const (
	defaultJobLease     = 2 * time.Minute
	defaultPollInterval = 30 * time.Second
	notifyBatchSize     = 16
)

type ingestionStore interface {
	claimIngestionJob(ctx context.Context, lease time.Duration) (ingestionJob, error)
	chunksAwaitingEmbedding(ctx context.Context, documentID string) ([]pendingChunk, error)
	completeIngestionJob(ctx context.Context, jobID string, embedded []embeddedChunk, model string) error
	failIngestionJob(ctx context.Context, job ingestionJob, reason string, permanent bool, backoff time.Duration) error
	documentChunks(ctx context.Context, documentID string) ([]pendingChunk, error)
	completeCardJob(ctx context.Context, jobID string, cards []newCard) error
}

type jobNotifier interface {
	Receive(ctx context.Context, count int64, block time.Duration) ([]queue.Message, error)
	Ack(ctx context.Context, ids ...string) error
}

type Worker struct {
	store     ingestionStore
	embedder  embeddingGenerator
	generator cardGenerator
	notifier  jobNotifier
	lease     time.Duration
	poll      time.Duration
	logger    *slog.Logger
}

func NewWorker(store *PostgresStore, embedder embeddingGenerator, generator cardGenerator, notifier jobNotifier) *Worker {
	return &Worker{
		store:     store,
		embedder:  embedder,
		generator: generator,
		notifier:  notifier,
		lease:     defaultJobLease,
		poll:      defaultPollInterval,
		logger:    slog.Default(),
	}
}

func (w *Worker) log() *slog.Logger {
	if w.logger == nil {
		return slog.New(slog.DiscardHandler)
	}

	return w.logger
}

func (w *Worker) Run(ctx context.Context) error {
	for {
		worked, err := w.ProcessOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.log().Error("process job", "error", err)
		}

		if worked && ctx.Err() == nil {
			continue
		}

		if err := w.waitForWork(ctx); err != nil {
			return err
		}
	}
}

func (w *Worker) waitForWork(ctx context.Context) error {
	if w.notifier == nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.poll):
		}

		return nil
	}

	messages, err := w.notifier.Receive(ctx, notifyBatchSize, w.poll)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.log().Error("receive notification", "error", err)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.poll):
		}

		return nil
	}

	for _, message := range messages {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		worked, err := w.ProcessOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.log().Error("process notified job", "error", err)
		}
		if err != nil {
			continue
		}
		if !worked {
			w.log().Warn("notified job was not in the queue", "job_id", message.JobID)
		}

		if err := w.notifier.Ack(ctx, message.ID); err != nil {
			w.log().Error("acknowledge notification", "notification_id", message.ID, "error", err)
		}
	}

	return ctx.Err()
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
	w.log().Info("job claimed", "job_id", job.ID, "kind", job.Kind, "document_id", job.DocumentID, "attempt", job.Attempts)

	if job.Kind == jobKindCards {
		return true, w.processCards(ctx, job, started)
	}

	pending, err := w.store.chunksAwaitingEmbedding(ctx, job.DocumentID)
	if err != nil {
		return true, w.fail(ctx, job, fmt.Sprintf("read pending chunks: %v", err), false)
	}

	if len(pending) == 0 {
		w.log().Info("nothing to embed", "job_id", job.ID, "document_id", job.DocumentID)
		return true, w.store.completeIngestionJob(ctx, job.ID, nil, embedding.Model)
	}

	w.log().Info("embedding chunks", "job_id", job.ID, "chunks", len(pending))

	inputs := make([]string, len(pending))
	for index, chunk := range pending {
		inputs[index] = chunk.Content
	}

	vectors, err := w.embedder.Embed(ctx, inputs)
	if err != nil {
		return true, w.fail(ctx, job, fmt.Sprintf("embed chunks: %v", err), permanentFailure(err))
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

	w.log().Info("job completed", "job_id", job.ID, "kind", job.Kind, "document_id", job.DocumentID, "chunks", len(embedded), "duration_ms", time.Since(started).Milliseconds())
	return true, nil
}

func (w *Worker) fail(ctx context.Context, job ingestionJob, reason string, permanent bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	backoff := retryBackoff(job.Attempts)
	terminal := permanent || job.Attempts >= maxIngestionAttempts
	w.log().Error("job failed",
		"job_id", job.ID,
		"kind", job.Kind,
		"document_id", job.DocumentID,
		"attempt", job.Attempts,
		"permanent", permanent,
		"terminal", terminal,
		"backoff_ms", backoff.Milliseconds(),
		"reason", reason,
	)

	if err := w.store.failIngestionJob(ctx, job, reason, permanent, backoff); err != nil {
		return fmt.Errorf("record job failure: %w", err)
	}

	return errors.New(reason)
}

func permanentFailure(err error) bool {
	var embeddingError *embedding.ProviderError
	if errors.As(err, &embeddingError) {
		return !embeddingError.Retryable()
	}

	var generationError *generation.ProviderError
	if errors.As(err, &generationError) {
		return !generationError.Retryable()
	}

	return false
}

func retryBackoff(attempts int) time.Duration {
	if attempts <= 1 {
		return 5 * time.Second
	}

	return 30 * time.Second
}
