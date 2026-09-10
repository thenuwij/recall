package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/thenujawijesuriya/recall/internal/embedding"
)

const jobFixtureDocumentID = "00000000-0000-0000-0000-0000000000fd"

// insertJobFixture creates one document with two unembedded chunks and a queued
// ingestion job, and removes them afterwards. The document cascade deletes both
// the chunks and the job.
func insertJobFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM documents WHERE id = $1`, jobFixtureDocumentID); err != nil {
			t.Errorf("clean up job fixture: %v", err)
		}
	})

	if _, err := pool.Exec(ctx,
		`INSERT INTO documents (id, content) VALUES ($1, 'ingestion job fixture')`,
		jobFixtureDocumentID,
	); err != nil {
		t.Fatalf("insert fixture document: %v", err)
	}

	for index, content := range []string{"first pending chunk", "second pending chunk"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO document_chunks (document_id, chunk_index, content) VALUES ($1, $2, $3)`,
			jobFixtureDocumentID, index, content,
		); err != nil {
			t.Fatalf("insert fixture chunk %d: %v", index, err)
		}
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO ingestion_jobs (document_id) VALUES ($1)`,
		jobFixtureDocumentID,
	); err != nil {
		t.Fatalf("insert fixture job: %v", err)
	}
}

func jobState(t *testing.T, pool *pgxpool.Pool, documentID string) (string, int) {
	t.Helper()

	var state string
	var attempts int
	if err := pool.QueryRow(context.Background(),
		`SELECT state, attempts FROM ingestion_jobs WHERE document_id = $1`,
		documentID,
	).Scan(&state, &attempts); err != nil {
		t.Fatalf("read job state: %v", err)
	}

	return state, attempts
}

func TestPostgresClaimIngestionJobLeasesAndCountsTheAttempt(t *testing.T) {
	pool := testPool(t)
	insertJobFixture(t, pool)
	store := NewPostgresStore(pool)

	job, err := store.claimIngestionJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("claimIngestionJob: %v", err)
	}
	if job.DocumentID != jobFixtureDocumentID {
		t.Fatalf("DocumentID = %q, want %q", job.DocumentID, jobFixtureDocumentID)
	}
	if job.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1: the attempt is counted when the job is claimed", job.Attempts)
	}

	state, _ := jobState(t, pool, jobFixtureDocumentID)
	if state != jobProcessing {
		t.Errorf("state = %q, want %q", state, jobProcessing)
	}
}

func TestPostgresClaimIngestionJobSkipsAnUnexpiredLease(t *testing.T) {
	pool := testPool(t)
	insertJobFixture(t, pool)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	if _, err := store.claimIngestionJob(ctx, time.Minute); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// A second worker must not be handed a job whose lease is still held.
	if _, err := store.claimIngestionJob(ctx, time.Minute); !errors.Is(err, errNoIngestionJob) {
		t.Fatalf("second claim error = %v, want errNoIngestionJob", err)
	}
}

func TestPostgresClaimIngestionJobReclaimsAnExpiredLease(t *testing.T) {
	pool := testPool(t)
	insertJobFixture(t, pool)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	// Claim with a lease that has already expired, standing in for a worker
	// that stalled or died while holding the job.
	if _, err := store.claimIngestionJob(ctx, -time.Minute); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	job, err := store.claimIngestionJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if job.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2: reclaiming counts a second attempt", job.Attempts)
	}
}

func TestPostgresChunksAwaitingEmbeddingSkipsEmbeddedChunks(t *testing.T) {
	pool := testPool(t)
	insertJobFixture(t, pool)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	pending, err := store.chunksAwaitingEmbedding(ctx, jobFixtureDocumentID)
	if err != nil {
		t.Fatalf("chunksAwaitingEmbedding: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %d, want 2", len(pending))
	}

	// Embed only the first chunk, as an interrupted attempt would have.
	if err := store.completeIngestionJob(ctx, jobIDFor(t, pool), []embeddedChunk{
		{ID: pending[0].ID, Embedding: testVector(1, 0)},
	}, embedding.Model); err != nil {
		t.Fatalf("completeIngestionJob: %v", err)
	}

	resumed, err := store.chunksAwaitingEmbedding(ctx, jobFixtureDocumentID)
	if err != nil {
		t.Fatalf("chunksAwaitingEmbedding after partial work: %v", err)
	}
	if len(resumed) != 1 {
		t.Fatalf("pending after partial work = %d, want 1: an embedded chunk must not be returned again", len(resumed))
	}
	if resumed[0].ID != pending[1].ID {
		t.Errorf("resumed chunk = %q, want the still-unembedded chunk %q", resumed[0].ID, pending[1].ID)
	}
}

func TestPostgresCompleteIngestionJobIsAtomic(t *testing.T) {
	pool := testPool(t)
	insertJobFixture(t, pool)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	pending, err := store.chunksAwaitingEmbedding(ctx, jobFixtureDocumentID)
	if err != nil {
		t.Fatalf("chunksAwaitingEmbedding: %v", err)
	}

	// The second vector has the wrong dimension, which pgvector rejects. The
	// first chunk's update and the job's completion must roll back with it.
	err = store.completeIngestionJob(ctx, jobIDFor(t, pool), []embeddedChunk{
		{ID: pending[0].ID, Embedding: testVector(1, 0)},
		{ID: pending[1].ID, Embedding: []float32{1, 2, 3}},
	}, embedding.Model)
	if err == nil {
		t.Fatal("completeIngestionJob() error = nil, want an error for a wrong-dimension vector")
	}

	state, _ := jobState(t, pool, jobFixtureDocumentID)
	if state == jobCompleted {
		t.Error("state = completed, want the job left incomplete after a failed vector write")
	}

	stillPending, err := store.chunksAwaitingEmbedding(ctx, jobFixtureDocumentID)
	if err != nil {
		t.Fatalf("chunksAwaitingEmbedding after failure: %v", err)
	}
	if len(stillPending) != 2 {
		t.Errorf("pending = %d, want 2: the first vector was committed despite a failed transaction", len(stillPending))
	}
}

func TestPostgresFailIngestionJobRequeuesThenGivesUp(t *testing.T) {
	pool := testPool(t)
	insertJobFixture(t, pool)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	// A retryable failure below the attempt cap returns the job to the queue.
	job := ingestionJob{ID: jobIDFor(t, pool), Attempts: 1}
	if err := store.failIngestionJob(ctx, job, "provider unavailable", false, 0); err != nil {
		t.Fatalf("failIngestionJob: %v", err)
	}
	if state, _ := jobState(t, pool, jobFixtureDocumentID); state != jobQueued {
		t.Errorf("state = %q, want %q after a retryable failure", state, jobQueued)
	}

	// The same failure at the attempt cap is terminal.
	job.Attempts = maxIngestionAttempts
	if err := store.failIngestionJob(ctx, job, "provider unavailable", false, 0); err != nil {
		t.Fatalf("failIngestionJob at cap: %v", err)
	}
	if state, _ := jobState(t, pool, jobFixtureDocumentID); state != jobFailed {
		t.Errorf("state = %q, want %q once attempts are exhausted", state, jobFailed)
	}
}

func TestPostgresFailIngestionJobTreatsPermanentFailureAsTerminal(t *testing.T) {
	pool := testPool(t)
	insertJobFixture(t, pool)
	store := NewPostgresStore(pool)

	// A permanent failure is terminal on the first attempt: retrying it would
	// repeat identical work to fail identically.
	job := ingestionJob{ID: jobIDFor(t, pool), Attempts: 1}
	if err := store.failIngestionJob(context.Background(), job, "malformed request", true, 0); err != nil {
		t.Fatalf("failIngestionJob: %v", err)
	}

	if state, _ := jobState(t, pool, jobFixtureDocumentID); state != jobFailed {
		t.Errorf("state = %q, want %q for a permanent failure", state, jobFailed)
	}
}

func jobIDFor(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()

	var id string
	if err := pool.QueryRow(context.Background(),
		`SELECT id::text FROM ingestion_jobs WHERE document_id = $1`,
		jobFixtureDocumentID,
	).Scan(&id); err != nil {
		t.Fatalf("read job id: %v", err)
	}

	return id
}
