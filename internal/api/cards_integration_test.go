package api

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func claimedCardJob(t *testing.T, store *PostgresStore, pool *pgxpool.Pool, documentID string) string {
	t.Helper()
	ctx := context.Background()

	var embedJobID string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM ingestion_jobs WHERE document_id = $1 AND kind = $2`,
		documentID, jobKindEmbed,
	).Scan(&embedJobID); err != nil {
		t.Fatalf("read embed job: %v", err)
	}
	if err := store.completeIngestionJob(ctx, embedJobID, nil, ""); err != nil {
		t.Fatalf("complete embed job: %v", err)
	}

	var cardJobID string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM ingestion_jobs WHERE document_id = $1 AND kind = $2`,
		documentID, jobKindCards,
	).Scan(&cardJobID); err != nil {
		t.Fatalf("read card job: %v", err)
	}

	if _, err := store.claimIngestionJob(ctx, time.Minute); err != nil {
		t.Fatalf("claim card job: %v", err)
	}

	return cardJobID
}

func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()

	var count int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}

	return count
}

func TestPostgresCompleteCardJobStoresCardsWithSchedules(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	id := createTestDocument(t, store, pool, newDocument{SourceType: sourceText, Content: "one two three four five six seven"})
	cardJobID := claimedCardJob(t, store, pool, id)

	chunks, err := store.documentChunks(ctx, id)
	if err != nil {
		t.Fatalf("documentChunks: %v", err)
	}
	if len(chunks) != 2 || chunks[0].Content != "one two three four" || chunks[1].Content != "four five six seven" {
		t.Fatalf("chunks = %+v, want both chunks in order", chunks)
	}

	before := time.Now()
	if err := store.completeCardJob(ctx, cardJobID, []newCard{
		{ChunkID: chunks[0].ID, Question: "What comes after three?", ExpectedAnswer: "Four."},
		{ChunkID: chunks[1].ID, Question: "What comes after six?", ExpectedAnswer: "Seven."},
	}); err != nil {
		t.Fatalf("completeCardJob: %v", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT s.due_at, s.interval_days, s.ease_factor, s.repetitions, s.lapses
		FROM cards k
		JOIN card_schedule s ON s.card_id = k.id
		JOIN document_chunks c ON c.id = k.chunk_id
		WHERE c.document_id = $1
	`, id)
	if err != nil {
		t.Fatalf("read schedules: %v", err)
	}
	defer rows.Close()

	schedules := 0
	for rows.Next() {
		var dueAt time.Time
		var interval, repetitions, lapses int
		var ease float64
		if err := rows.Scan(&dueAt, &interval, &ease, &repetitions, &lapses); err != nil {
			t.Fatalf("scan schedule: %v", err)
		}
		schedules++
		if dueAt.After(time.Now()) || dueAt.Before(before.Add(-time.Minute)) {
			t.Errorf("due_at = %v, want due now", dueAt)
		}
		if interval != 0 || ease != 2.5 || repetitions != 0 || lapses != 0 {
			t.Errorf("schedule = %d days, ease %v, %d repetitions, %d lapses, want a new card", interval, ease, repetitions, lapses)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read schedules: %v", err)
	}
	if schedules != 2 {
		t.Fatalf("cards with schedules = %d, want 2", schedules)
	}

	stored, err := store.getDocument(ctx, id, testOwnerID)
	if err != nil {
		t.Fatalf("getDocument: %v", err)
	}
	if stored.CardsStatus != statusReady || stored.CardCount != 2 {
		t.Fatalf("cards_status = %q, card_count = %d, want %q and 2", stored.CardsStatus, stored.CardCount, statusReady)
	}
}

func TestPostgresCompleteCardJobIsAtomic(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	id := createTestDocument(t, store, pool, newDocument{SourceType: sourceText, Content: "one two three"})
	cardJobID := claimedCardJob(t, store, pool, id)

	chunks, err := store.documentChunks(ctx, id)
	if err != nil {
		t.Fatalf("documentChunks: %v", err)
	}

	err = store.completeCardJob(ctx, cardJobID, []newCard{
		{ChunkID: chunks[0].ID, Question: "What comes after two?", ExpectedAnswer: "Three."},
		{ChunkID: "00000000-0000-0000-0000-000000000000", Question: "A card for a missing chunk?", ExpectedAnswer: "Rejected."},
	})
	if err == nil {
		t.Fatal("completeCardJob() error = nil, want the missing chunk rejected")
	}

	cards := countRows(t, pool, `SELECT count(*) FROM cards WHERE chunk_id = $1`, chunks[0].ID)
	schedules := countRows(t, pool, `SELECT count(*) FROM card_schedule s JOIN cards k ON k.id = s.card_id WHERE k.chunk_id = $1`, chunks[0].ID)
	if cards != 0 || schedules != 0 {
		t.Fatalf("cards = %d, schedules = %d, want the first card rolled back", cards, schedules)
	}

	stored, err := store.getDocument(ctx, id, testOwnerID)
	if err != nil {
		t.Fatalf("getDocument: %v", err)
	}
	if stored.CardsStatus == statusReady {
		t.Fatal("cards_status = ready, want the job left incomplete after a failed insert")
	}
}

func TestPostgresDeleteDocumentCascadesToCards(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	id := createTestDocument(t, store, pool, newDocument{SourceType: sourceText, Content: "one two three"})
	cardJobID := claimedCardJob(t, store, pool, id)

	chunks, err := store.documentChunks(ctx, id)
	if err != nil {
		t.Fatalf("documentChunks: %v", err)
	}
	if err := store.completeCardJob(ctx, cardJobID, []newCard{
		{ChunkID: chunks[0].ID, Question: "What comes after two?", ExpectedAnswer: "Three."},
	}); err != nil {
		t.Fatalf("completeCardJob: %v", err)
	}

	var cardID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM cards WHERE chunk_id = $1`, chunks[0].ID).Scan(&cardID); err != nil {
		t.Fatalf("read card: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO reviews (card_id, user_answer, grade, rationale) VALUES ($1, 'three', 5, 'Correct.')`,
		cardID,
	); err != nil {
		t.Fatalf("insert review: %v", err)
	}

	if err := store.deleteDocument(ctx, id, testOwnerID); err != nil {
		t.Fatalf("deleteDocument: %v", err)
	}

	cards := countRows(t, pool, `SELECT count(*) FROM cards WHERE id = $1`, cardID)
	schedules := countRows(t, pool, `SELECT count(*) FROM card_schedule WHERE card_id = $1`, cardID)
	reviews := countRows(t, pool, `SELECT count(*) FROM reviews WHERE card_id = $1`, cardID)
	if cards != 0 || schedules != 0 || reviews != 0 {
		t.Fatalf("after delete: cards = %d, schedules = %d, reviews = %d, want all 0", cards, schedules, reviews)
	}
}
