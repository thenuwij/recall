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
	if err := store.completeCardJob(ctx, jobID, 1, cards); err != nil {
		t.Fatalf("first completion: %v", err)
	}

	err := store.completeCardJob(ctx, jobID, 1, cards)

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

func TestPostgresRenewIngestionJobExtendsTheLease(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	createTestDocument(t, store, pool, newDocument{Title: "Renew", SourceType: sourceText, Content: "one two three four"})

	job, err := store.claimIngestionJob(ctx, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	var before time.Time
	if err := pool.QueryRow(ctx, `SELECT claimed_until FROM ingestion_jobs WHERE id = $1`, job.ID).Scan(&before); err != nil {
		t.Fatalf("read lease: %v", err)
	}

	if err := store.renewIngestionJob(ctx, job.ID, job.Attempts, 5*time.Minute); err != nil {
		t.Fatalf("renew: %v", err)
	}

	var after time.Time
	if err := pool.QueryRow(ctx, `SELECT claimed_until FROM ingestion_jobs WHERE id = $1`, job.ID).Scan(&after); err != nil {
		t.Fatalf("read lease: %v", err)
	}
	if !after.After(before) {
		t.Fatalf("claimed_until %v did not move past %v", after, before)
	}
}

func TestPostgresRenewIngestionJobRefusesAJobItNoLongerHolds(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	id := createTestDocument(t, store, pool, newDocument{Title: "Lost", SourceType: sourceText, Content: "one two three four"})
	jobID := claimedCardJob(t, store, pool, id)

	if err := store.completeCardJob(ctx, jobID, 1, nil); err != nil {
		t.Fatalf("complete: %v", err)
	}

	err := store.renewIngestionJob(ctx, jobID, 1, time.Minute)

	if !errors.Is(err, errJobNoLongerHeld) {
		t.Fatalf("error = %v, want %v", err, errJobNoLongerHeld)
	}
}

func TestPostgresNextDueAtReportsWhenCappedCardsUnlock(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	id := createTestDocument(t, store, pool, newDocument{Title: "Capped", SourceType: sourceText, Content: "one two three four five six seven eight"})
	jobID := claimedCardJob(t, store, pool, id)

	chunkID := firstChunkID(t, pool, id)
	cards := []newCard{
		{ChunkID: chunkID, Question: "Reviewed?", ExpectedAnswer: "Yes."},
		{ChunkID: chunkID, Question: "Waiting?", ExpectedAnswer: "Yes."},
	}
	if err := store.completeCardJob(ctx, jobID, 1, cards); err != nil {
		t.Fatalf("store cards: %v", err)
	}

	var reviewedCardID string
	if err := pool.QueryRow(ctx,
		`SELECT k.id::text FROM cards k JOIN document_chunks c ON c.id = k.chunk_id
		 WHERE c.document_id = $1 AND k.question = 'Reviewed?'`, id,
	).Scan(&reviewedCardID); err != nil {
		t.Fatalf("read card: %v", err)
	}

	firstReviewedAt := time.Now().Add(-2 * time.Hour)
	if _, err := pool.Exec(ctx,
		`INSERT INTO reviews (card_id, user_answer, grade, reviewed_at) VALUES ($1, 'an answer', 4, $2)`,
		reviewedCardID, firstReviewedAt,
	); err != nil {
		t.Fatalf("record review: %v", err)
	}

	next, err := store.nextDueAt(ctx, testOwnerID, 1)
	if err != nil {
		t.Fatalf("nextDueAt: %v", err)
	}
	if next == nil {
		t.Fatal("next_due_at is nil, but a new card is waiting behind the daily cap")
	}

	want := firstReviewedAt.Add(24 * time.Hour)
	if next.Sub(want) > time.Minute || want.Sub(*next) > time.Minute {
		t.Fatalf("next_due_at = %v, want about %v (when the cap frees a slot)", next, want)
	}
}
