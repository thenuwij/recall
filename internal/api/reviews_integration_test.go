package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/thenujawijesuriya/recall/internal/generation"
	"github.com/thenujawijesuriya/recall/internal/scheduling"
)

func createTestCards(t *testing.T, store *PostgresStore, pool *pgxpool.Pool, count int) []string {
	t.Helper()
	ctx := context.Background()

	words := make([]string, 3*count+1)
	for index := range words {
		words[index] = fmt.Sprintf("w%d", index)
	}
	id := createTestDocument(t, store, pool, newDocument{Title: "Review fixture", SourceType: sourceText, Content: strings.Join(words, " ")})
	cardJobID := claimedCardJob(t, store, pool, id)

	chunks, err := store.documentChunks(ctx, id)
	if err != nil {
		t.Fatalf("documentChunks: %v", err)
	}
	if len(chunks) != count {
		t.Fatalf("chunks = %d, want %d", len(chunks), count)
	}

	cards := make([]newCard, count)
	for index, chunk := range chunks {
		cards[index] = newCard{ChunkID: chunk.ID, Question: fmt.Sprintf("Question %d?", index), ExpectedAnswer: fmt.Sprintf("Answer %d.", index)}
	}
	if err := store.completeCardJob(ctx, cardJobID, cards); err != nil {
		t.Fatalf("completeCardJob: %v", err)
	}

	ids := make([]string, count)
	for index, chunk := range chunks {
		if err := pool.QueryRow(ctx, `SELECT id::text FROM cards WHERE chunk_id = $1`, chunk.ID).Scan(&ids[index]); err != nil {
			t.Fatalf("read card %d: %v", index, err)
		}
	}

	return ids
}

func setDue(t *testing.T, pool *pgxpool.Pool, cardID string, offset time.Duration) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`UPDATE card_schedule SET due_at = now() + make_interval(secs => $2) WHERE card_id = $1`,
		cardID, offset.Seconds(),
	); err != nil {
		t.Fatalf("set due_at: %v", err)
	}
}

func insertReviewAt(t *testing.T, pool *pgxpool.Pool, cardID string, age time.Duration) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`INSERT INTO reviews (card_id, user_answer, grade, reviewed_at) VALUES ($1, 'answer', 4, now() - make_interval(secs => $2))`,
		cardID, age.Seconds(),
	); err != nil {
		t.Fatalf("insert review: %v", err)
	}
}

func ownDueCards(t *testing.T, store *PostgresStore, limit int, ids []string) []dueCard {
	t.Helper()

	cards, err := store.dueCards(context.Background(), limit, newCardsPerDay, testOwnerID)
	if err != nil {
		t.Fatalf("dueCards: %v", err)
	}

	own := make(map[string]bool, len(ids))
	for _, id := range ids {
		own[id] = true
	}
	var result []dueCard
	for _, card := range cards {
		if own[card.CardID] {
			result = append(result, card)
		}
	}

	return result
}

func TestPostgresDueCardsOrdersReviewsBeforeNewCards(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)

	ids := createTestCards(t, store, pool, 4)
	insertReviewAt(t, pool, ids[1], time.Hour)
	setDue(t, pool, ids[1], -time.Hour)
	insertReviewAt(t, pool, ids[2], time.Hour)
	setDue(t, pool, ids[2], -2*time.Hour)
	insertReviewAt(t, pool, ids[3], time.Hour)
	setDue(t, pool, ids[3], 72*time.Hour)

	cards := ownDueCards(t, store, 10, ids)

	if len(cards) != 3 {
		t.Fatalf("due cards = %+v, want 3", cards)
	}
	if cards[0].CardID != ids[2] || cards[1].CardID != ids[1] || cards[2].CardID != ids[0] {
		t.Fatalf("order = %s, %s, %s; want the oldest due review, the next review, then the new card", cards[0].CardID, cards[1].CardID, cards[2].CardID)
	}
	if cards[0].IsNew || cards[1].IsNew || !cards[2].IsNew {
		t.Fatalf("is_new = %t, %t, %t; want false, false, true", cards[0].IsNew, cards[1].IsNew, cards[2].IsNew)
	}
	if cards[2].Question != "Question 0?" || cards[2].Title != "Review fixture" {
		t.Fatalf("new card = %+v", cards[2])
	}

	var futureDue time.Time
	if err := pool.QueryRow(context.Background(), `SELECT due_at FROM card_schedule WHERE card_id = $1`, ids[3]).Scan(&futureDue); err != nil {
		t.Fatalf("read due_at: %v", err)
	}
	next, err := store.nextDueAt(context.Background(), testOwnerID)
	if err != nil {
		t.Fatalf("nextDueAt: %v", err)
	}
	if next == nil || !next.Equal(futureDue) {
		t.Fatalf("next due = %v, want %v", next, futureDue)
	}
}

func TestPostgresDueCardsCapsNewCardsPerDay(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)

	ids := createTestCards(t, store, pool, 25)

	cards := ownDueCards(t, store, 100, ids)
	if len(cards) != newCardsPerDay {
		t.Fatalf("new cards = %d, want %d", len(cards), newCardsPerDay)
	}
	for index, card := range cards {
		if card.CardID != ids[index] || !card.IsNew {
			t.Fatalf("card %d = %+v, want new card %s in chunk order", index, card, ids[index])
		}
	}

	for _, id := range ids[:5] {
		insertReviewAt(t, pool, id, time.Minute)
		setDue(t, pool, id, 24*time.Hour)
	}
	if cards := ownDueCards(t, store, 100, ids); len(cards) != 15 || cards[0].CardID != ids[5] {
		t.Fatalf("after introducing 5: %d new cards starting at %v, want 15 starting at %s", len(cards), cards, ids[5])
	}

	if _, err := pool.Exec(context.Background(), `UPDATE reviews SET reviewed_at = now() - interval '25 hours' WHERE card_id = $1`, ids[0]); err != nil {
		t.Fatalf("age review: %v", err)
	}
	if cards := ownDueCards(t, store, 100, ids); len(cards) != 16 {
		t.Fatalf("after one introduction left the window: %d new cards, want 16", len(cards))
	}

	if cards := ownDueCards(t, store, 3, ids); len(cards) != 3 {
		t.Fatalf("with limit 3: %d cards, want 3", len(cards))
	}
}

func TestPostgresDueCardsDoesNotCapReviews(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)

	ids := createTestCards(t, store, pool, 22)
	for _, id := range ids[:21] {
		insertReviewAt(t, pool, id, time.Minute)
		setDue(t, pool, id, -time.Minute)
	}

	cards := ownDueCards(t, store, 100, ids)
	if len(cards) != 21 {
		t.Fatalf("due cards = %d, want all 21 reviews and no new card once the cap is used", len(cards))
	}
	for _, card := range cards {
		if card.IsNew {
			t.Fatalf("card %s is new, want reviews only", card.CardID)
		}
	}
}

func TestPostgresRecordReviewUpdatesTheScheduleOnce(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	ids := createTestCards(t, store, pool, 1)

	card, err := store.reviewCard(ctx, ids[0], testOwnerID)
	if err != nil {
		t.Fatalf("reviewCard: %v", err)
	}
	if card.Question != "Question 0?" || card.ExpectedAnswer != "Answer 0." || card.Passage != "w0 w1 w2 w3" {
		t.Fatalf("card = %+v", card)
	}
	if card.Schedule != (scheduling.State{EaseFactor: 2.5}) {
		t.Fatalf("schedule = %+v, want a new card", card.Schedule)
	}
	if card.Location.Title != "Review fixture" || card.Location.Start == nil || card.Location.End == nil {
		t.Fatalf("location = %+v", card.Location)
	}

	schedule, dueAt := scheduling.Next(card.Schedule, 4, time.Now())
	review := newReview{
		CardID:            ids[0],
		Answer:            "w three",
		Grade:             generation.Grade{Score: 4, Rationale: "Close."},
		Schedule:          schedule,
		DueAt:             dueAt,
		PreviousUpdatedAt: card.UpdatedAt,
	}
	if err := store.recordReview(ctx, review); err != nil {
		t.Fatalf("recordReview: %v", err)
	}

	updated, err := store.reviewCard(ctx, ids[0], testOwnerID)
	if err != nil {
		t.Fatalf("reviewCard after review: %v", err)
	}
	if updated.Schedule != schedule {
		t.Fatalf("schedule = %+v, want %+v", updated.Schedule, schedule)
	}
	if !updated.UpdatedAt.After(card.UpdatedAt) {
		t.Fatalf("updated_at = %v, want later than %v", updated.UpdatedAt, card.UpdatedAt)
	}

	if err := store.recordReview(ctx, review); !errors.Is(err, errReviewConflict) {
		t.Fatalf("second recordReview error = %v, want %v", err, errReviewConflict)
	}
	if reviews := countRows(t, pool, `SELECT count(*) FROM reviews WHERE card_id = $1`, ids[0]); reviews != 1 {
		t.Fatalf("reviews = %d, want 1", reviews)
	}

	if _, err := store.reviewCard(ctx, "00000000-0000-0000-0000-000000000000", testOwnerID); !errors.Is(err, errCardNotFound) {
		t.Fatalf("unknown card error = %v, want %v", err, errCardNotFound)
	}
}

func TestPostgresRecordReviewAcceptsOneOfTwoConcurrentSubmissions(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	ids := createTestCards(t, store, pool, 1)
	card, err := store.reviewCard(ctx, ids[0], testOwnerID)
	if err != nil {
		t.Fatalf("reviewCard: %v", err)
	}
	schedule, dueAt := scheduling.Next(card.Schedule, 5, time.Now())

	var wait sync.WaitGroup
	errs := make([]error, 2)
	for index := range errs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs[index] = store.recordReview(ctx, newReview{
				CardID:            ids[0],
				Answer:            fmt.Sprintf("submission %d", index),
				Grade:             generation.Grade{Score: 5, Rationale: "Complete."},
				Schedule:          schedule,
				DueAt:             dueAt,
				PreviousUpdatedAt: card.UpdatedAt,
			})
		}()
	}
	wait.Wait()

	succeeded, conflicted := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, errReviewConflict):
			conflicted++
		default:
			t.Fatalf("recordReview: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded = %d, conflicted = %d, want 1 and 1", succeeded, conflicted)
	}
	if reviews := countRows(t, pool, `SELECT count(*) FROM reviews WHERE card_id = $1`, ids[0]); reviews != 1 {
		t.Fatalf("reviews = %d, want 1", reviews)
	}
}
