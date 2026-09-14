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
		`SELECT state, attempts FROM ingestion_jobs WHERE document_id = $1 AND kind = $2`,
		documentID, jobKindEmbed,
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
	if job.Kind != jobKindEmbed {
		t.Errorf("Kind = %q, want %q", job.Kind, jobKindEmbed)
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

	if _, err := store.claimIngestionJob(ctx, time.Minute); !errors.Is(err, errNoIngestionJob) {
		t.Fatalf("second claim error = %v, want errNoIngestionJob", err)
	}
}

func TestPostgresClaimIngestionJobReclaimsAnExpiredLease(t *testing.T) {
	pool := testPool(t)
	insertJobFixture(t, pool)
	store := NewPostgresStore(pool)
	ctx := context.Background()

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

	if _, err := store.claimIngestionJob(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.completeIngestionJob(ctx, jobIDFor(t, pool), 1, []embeddedChunk{
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

	if _, err := store.claimIngestionJob(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	err = store.completeIngestionJob(ctx, jobIDFor(t, pool), 1, []embeddedChunk{
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

	job, err := store.claimIngestionJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.failIngestionJob(ctx, job, "provider unavailable", false, 0); err != nil {
		t.Fatalf("failIngestionJob: %v", err)
	}
	if state, _ := jobState(t, pool, jobFixtureDocumentID); state != jobQueued {
		t.Errorf("state = %q, want %q after a retryable failure", state, jobQueued)
	}

	job, err = store.claimIngestionJob(ctx, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.claimIngestionJob(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
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

	job, err := store.claimIngestionJob(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
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
		`SELECT id::text FROM ingestion_jobs WHERE document_id = $1 AND kind = $2`,
		jobFixtureDocumentID, jobKindEmbed,
	).Scan(&id); err != nil {
		t.Fatalf("read job id: %v", err)
	}

	return id
}

func jobKinds(t *testing.T, pool *pgxpool.Pool, documentID string) map[string]string {
	t.Helper()

	rows, err := pool.Query(context.Background(),
		`SELECT kind, state FROM ingestion_jobs WHERE document_id = $1`,
		documentID,
	)
	if err != nil {
		t.Fatalf("read jobs: %v", err)
	}
	defer rows.Close()

	kinds := make(map[string]string)
	for rows.Next() {
		var kind, state string
		if err := rows.Scan(&kind, &state); err != nil {
			t.Fatalf("scan job: %v", err)
		}
		kinds[kind] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read jobs: %v", err)
	}

	return kinds
}

func TestPostgresCompleteIngestionJobChainsOneCardJob(t *testing.T) {
	pool := testPool(t)
	insertJobFixture(t, pool)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	embedJob, err := store.claimIngestionJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("claim embed job: %v", err)
	}
	if err := store.completeIngestionJob(ctx, embedJob.ID, embedJob.Attempts, nil, embedding.Model); err != nil {
		t.Fatalf("complete embed job: %v", err)
	}

	kinds := jobKinds(t, pool, jobFixtureDocumentID)
	if len(kinds) != 2 || kinds[jobKindEmbed] != jobCompleted || kinds[jobKindCards] != jobQueued {
		t.Fatalf("jobs = %v, want a completed embed job and a queued card job", kinds)
	}

	if err := store.completeIngestionJob(ctx, embedJob.ID, embedJob.Attempts, nil, embedding.Model); !errors.Is(err, errJobNoLongerHeld) {
		t.Fatalf("repeated completion should lose ownership: %v", err)
	}

	cardJob, err := store.claimIngestionJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("claim card job: %v", err)
	}
	if cardJob.Kind != jobKindCards || cardJob.DocumentID != jobFixtureDocumentID {
		t.Fatalf("claimed %+v, want the card job for the fixture document", cardJob)
	}
	if err := store.completeIngestionJob(ctx, cardJob.ID, cardJob.Attempts, nil, ""); err != nil {
		t.Fatalf("complete card job: %v", err)
	}

	kinds = jobKinds(t, pool, jobFixtureDocumentID)
	if len(kinds) != 2 || kinds[jobKindEmbed] != jobCompleted || kinds[jobKindCards] != jobCompleted {
		t.Fatalf("jobs = %v, want exactly one completed job of each kind", kinds)
	}
}
