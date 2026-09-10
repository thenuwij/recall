package api

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Ingestion job states. PostgreSQL enforces this set with a CHECK constraint;
// these constants exist so Go never spells one of them differently.
const (
	jobQueued     = "queued"
	jobProcessing = "processing"
	jobCompleted  = "completed"
	jobFailed     = "failed"
)

// maxIngestionAttempts bounds retries of a retryable failure. Three attempts
// means the original try plus two retries.
const maxIngestionAttempts = 3

var errNoIngestionJob = errors.New("no ingestion job available")

// ingestionJob is one document's outstanding embedding work.
type ingestionJob struct {
	ID         string
	DocumentID string
	Attempts   int
}

// pendingChunk is a stored chunk that has no embedding yet.
type pendingChunk struct {
	ID      string
	Content string
}

// embeddedChunk carries a vector back to the chunk it belongs to.
type embeddedChunk struct {
	ID        string
	Embedding []float32
}

// claimIngestionJob atomically takes the oldest claimable job and leases it for
// the given duration. A job is claimable when it is queued and its claimed_until
// has passed, or when it is processing and its lease expired because the worker
// holding it stalled or died. SKIP LOCKED lets several workers claim different
// jobs concurrently without blocking each other.
func (s *PostgresStore) claimIngestionJob(ctx context.Context, lease time.Duration) (ingestionJob, error) {
	const claimQuery = `
		UPDATE ingestion_jobs
		SET state = $1,
		    attempts = attempts + 1,
		    claimed_until = now() + make_interval(secs => $2),
		    updated_at = now()
		WHERE id = (
			SELECT id
			FROM ingestion_jobs
			WHERE (state = $3 AND (claimed_until IS NULL OR claimed_until <= now()))
			   OR (state = $1 AND claimed_until <= now())
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id::text, document_id::text, attempts
	`

	var job ingestionJob
	err := s.pool.QueryRow(ctx, claimQuery, jobProcessing, lease.Seconds(), jobQueued).
		Scan(&job.ID, &job.DocumentID, &job.Attempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ingestionJob{}, errNoIngestionJob
		}
		return ingestionJob{}, err
	}

	return job, nil
}

// chunksAwaitingEmbedding returns the document's chunks that still have no
// vector. A retry therefore resumes rather than restarting, because chunks
// embedded by an earlier attempt are already excluded.
func (s *PostgresStore) chunksAwaitingEmbedding(ctx context.Context, documentID string) ([]pendingChunk, error) {
	const pendingQuery = `
		SELECT id::text, content
		FROM document_chunks
		WHERE document_id = $1
		    AND embedding IS NULL
		ORDER BY chunk_index
	`

	rows, err := s.pool.Query(ctx, pendingQuery, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chunks := make([]pendingChunk, 0)
	for rows.Next() {
		var chunk pendingChunk
		if err := rows.Scan(&chunk.ID, &chunk.Content); err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return chunks, nil
}

// completeIngestionJob writes every vector and marks the job completed in one
// transaction, so a completed job can never claim work that is not durable.
func (s *PostgresStore) completeIngestionJob(ctx context.Context, jobID string, embedded []embeddedChunk, model string) error {
	transaction, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	const updateChunkQuery = `
		UPDATE document_chunks
		SET embedding = $2::vector,
		    embedding_model = $3,
		    embedded_at = now()
		WHERE id = $1
	`
	for _, chunk := range embedded {
		if _, err := transaction.Exec(ctx, updateChunkQuery, chunk.ID, formatVector(chunk.Embedding), model); err != nil {
			return err
		}
	}

	const completeQuery = `
		UPDATE ingestion_jobs
		SET state = $2,
		    claimed_until = NULL,
		    last_error = NULL,
		    updated_at = now()
		WHERE id = $1
	`
	if _, err := transaction.Exec(ctx, completeQuery, jobID, jobCompleted); err != nil {
		return err
	}

	return transaction.Commit(ctx)
}

// failIngestionJob records why an attempt failed. A permanent failure, or one
// that has exhausted its attempts, becomes terminal and is left for inspection.
// Anything else returns to the queue, held back by claimed_until so the retry
// backs off instead of hammering a provider that just rejected it.
func (s *PostgresStore) failIngestionJob(ctx context.Context, job ingestionJob, reason string, permanent bool, backoff time.Duration) error {
	terminal := permanent || job.Attempts >= maxIngestionAttempts

	if terminal {
		const failQuery = `
			UPDATE ingestion_jobs
			SET state = $2,
			    claimed_until = NULL,
			    last_error = $3,
			    updated_at = now()
			WHERE id = $1
		`
		_, err := s.pool.Exec(ctx, failQuery, job.ID, jobFailed, reason)
		return err
	}

	const requeueQuery = `
		UPDATE ingestion_jobs
		SET state = $2,
		    claimed_until = now() + make_interval(secs => $3),
		    last_error = $4,
		    updated_at = now()
		WHERE id = $1
	`
	_, err := s.pool.Exec(ctx, requeueQuery, job.ID, jobQueued, backoff.Seconds(), reason)
	return err
}
