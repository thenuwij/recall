package api

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	jobQueued     = "queued"
	jobProcessing = "processing"
	jobCompleted  = "completed"
	jobFailed     = "failed"
)

const (
	jobKindEmbed = "embed"
	jobKindCards = "generate_cards"
)

const maxIngestionAttempts = 3

const (
	statusQueued     = "queued"
	statusProcessing = "processing"
	statusReady      = "ready"
	statusFailed     = "failed"
	statusUnknown    = "unknown"
	statusNotStarted = "not_started"
)

func ingestionStatus(state *string) string {
	if state == nil {
		return statusUnknown
	}

	switch *state {
	case jobQueued:
		return statusQueued
	case jobProcessing:
		return statusProcessing
	case jobCompleted:
		return statusReady
	case jobFailed:
		return statusFailed
	}

	return statusUnknown
}

func cardsStatus(state *string) string {
	if state == nil {
		return statusNotStarted
	}

	return ingestionStatus(state)
}

var errNoIngestionJob = errors.New("no ingestion job available")

var errJobNoLongerHeld = errors.New("ingestion job is no longer held by this worker")

type ingestionJob struct {
	ID         string
	DocumentID string
	Kind       string
	Attempts   int
}

type pendingChunk struct {
	ID      string
	Content string
}

type embeddedChunk struct {
	ID        string
	Embedding []float32
}

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
		RETURNING id::text, document_id::text, kind, attempts
	`

	var job ingestionJob
	err := s.pool.QueryRow(ctx, claimQuery, jobProcessing, lease.Seconds(), jobQueued).
		Scan(&job.ID, &job.DocumentID, &job.Kind, &job.Attempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ingestionJob{}, errNoIngestionJob
		}
		return ingestionJob{}, err
	}

	return job, nil
}

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

	const chainCardsQuery = `
		INSERT INTO ingestion_jobs (document_id, kind)
		SELECT document_id, $2
		FROM ingestion_jobs
		WHERE id = $1
		    AND kind = $3
		ON CONFLICT (document_id, kind) DO NOTHING
	`
	if _, err := transaction.Exec(ctx, chainCardsQuery, jobID, jobKindCards, jobKindEmbed); err != nil {
		return err
	}

	return transaction.Commit(ctx)
}

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

func (s *PostgresStore) renewIngestionJob(ctx context.Context, jobID string, lease time.Duration) error {
	const query = `
		UPDATE ingestion_jobs
		SET claimed_until = now() + make_interval(secs => $2),
		    updated_at = now()
		WHERE id = $1 AND state = $3
	`

	result, err := s.pool.Exec(ctx, query, jobID, lease.Seconds(), jobProcessing)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errJobNoLongerHeld
	}

	return nil
}
