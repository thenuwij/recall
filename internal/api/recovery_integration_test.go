package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresCrashedEmbedJobIsRecoveredAndCompletesOnce(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	id := createTestDocument(t, store, pool, newDocument{Title: "Crash", SourceType: sourceText, Content: "one two three four five six seven eight"})

	crashed, err := store.claimIngestionJob(ctx, -time.Minute)
	if err != nil {
		t.Fatalf("claim as the doomed worker: %v", err)
	}
	if crashed.DocumentID != id {
		t.Skipf("claimed another document's job (%s), the database is not quiet enough for this test", crashed.DocumentID)
	}

	worker := &Worker{store: store, embedder: &fakeEmbedder{}, lease: time.Minute, poll: time.Millisecond}
	worked, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("recovering worker: %v", err)
	}
	if !worked {
		t.Fatal("recovering worker found no work, the crashed job was lost")
	}

	var state string
	var attempts int
	if err := pool.QueryRow(ctx,
		`SELECT state, attempts FROM ingestion_jobs WHERE id = $1`, crashed.ID,
	).Scan(&state, &attempts); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if state != jobCompleted {
		t.Fatalf("state = %q, want %q", state, jobCompleted)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2: the crash should count one attempt and the recovery another", attempts)
	}

	embedded := countRows(t, pool,
		`SELECT count(*) FROM document_chunks WHERE document_id = $1 AND embedding IS NOT NULL`, id)
	total := countRows(t, pool, `SELECT count(*) FROM document_chunks WHERE document_id = $1`, id)
	if embedded != total {
		t.Fatalf("embedded %d of %d chunks, want every chunk embedded exactly once", embedded, total)
	}
}

func TestPostgresCompleteCardJobRefusesASecondCompletion(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	id := createTestDocument(t, store, pool, newDocument{Title: "Duplicate", SourceType: sourceText, Content: "one two three four five six"})
	jobID := claimedCardJob(t, store, pool, id)

	cards := []newCard{{ChunkID: firstChunkID(t, pool, id), Question: "Q?", ExpectedAnswer: "A."}}
	if err := store.completeCardJob(ctx, jobID, cards); err != nil {
		t.Fatalf("first completion: %v", err)
	}

	err := store.completeCardJob(ctx, jobID, cards)

	if !errors.Is(err, errJobNoLongerHeld) {
		t.Fatalf("second completion error = %v, want %v", err, errJobNoLongerHeld)
	}
	stored := countRows(t, pool,
		`SELECT count(*) FROM cards k JOIN document_chunks c ON c.id = k.chunk_id WHERE c.document_id = $1`, id)
	if stored != 1 {
		t.Fatalf("cards = %d, want 1: a repeated completion must not duplicate cards", stored)
	}
}

func TestPostgresJobIsClaimedWithoutANotification(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	id := createTestDocument(t, store, pool, newDocument{Title: "No notification", SourceType: sourceText, Content: "one two three four"})

	job, err := store.claimIngestionJob(ctx, time.Minute)
	if err != nil {
		t.Fatalf("claim without any Redis message: %v", err)
	}
	if job.DocumentID != id {
		t.Skipf("claimed another document's job (%s), the database is not quiet enough for this test", job.DocumentID)
	}
}

func firstChunkID(t *testing.T, pool *pgxpool.Pool, documentID string) string {
	t.Helper()

	var id string
	if err := pool.QueryRow(context.Background(),
		`SELECT id::text FROM document_chunks WHERE document_id = $1 ORDER BY chunk_index LIMIT 1`, documentID,
	).Scan(&id); err != nil {
		t.Fatalf("read first chunk: %v", err)
	}

	return id
}
