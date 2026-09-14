package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/thenujawijesuriya/recall/internal/generation"
	"github.com/thenujawijesuriya/recall/internal/scheduling"
)

const (
	defaultDueLimit           = 20
	maxDueLimit               = 100
	newCardsPerDay            = 20
	maxReviewAnswerCharacters = 2000
)

var (
	errCardNotFound   = errors.New("card not found")
	errReviewConflict = errors.New("card schedule changed since it was loaded")
)

type dueCard struct {
	CardID   string `json:"card_id"`
	Question string `json:"question"`
	Title    string `json:"title,omitempty"`
	Page     *int   `json:"page,omitempty"`
	IsNew    bool   `json:"is_new"`
}

type dueCardsResponse struct {
	Cards     []dueCard  `json:"cards"`
	NextDueAt *time.Time `json:"next_due_at"`
}

type reviewCard struct {
	Question       string
	ExpectedAnswer string
	Passage        string
	Location       chunkLocation
	Schedule       scheduling.State
	UpdatedAt      time.Time
}

type newReview struct {
	CardID            string
	Answer            string
	Grade             generation.Grade
	Schedule          scheduling.State
	DueAt             time.Time
	PreviousUpdatedAt time.Time
}

type reviewRequest struct {
	Answer string `json:"answer"`
}

type reviewResponse struct {
	Score          int                     `json:"score"`
	Label          string                  `json:"label"`
	Rationale      string                  `json:"rationale"`
	ExpectedAnswer string                  `json:"expected_answer"`
	Source         *passageContextResponse `json:"source,omitempty"`
	IntervalDays   int                     `json:"interval_days"`
	NextDueAt      time.Time               `json:"next_due_at"`
}

func gradeLabel(score int) string {
	switch {
	case score <= 1:
		return "Forgot"
	case score == 2:
		return "Almost"
	case score == 3:
		return "Hard"
	case score == 4:
		return "Good"
	}

	return "Easy"
}

func (h *handler) dueReviews(w http.ResponseWriter, r *http.Request) {
	limit := defaultDueLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxDueLimit {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "limit must be between 1 and 100"})
			return
		}
		limit = parsed
	}

	account, _ := userFromContext(r.Context())
	cards, err := h.store.dueCards(r.Context(), limit, newCardsPerDay, account.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not load due cards"})
		return
	}
	if cards == nil {
		cards = make([]dueCard, 0)
	}

	nextDueAt, err := h.store.nextDueAt(r.Context(), account.ID, newCardsPerDay)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not load due cards"})
		return
	}

	writeJSON(w, http.StatusOK, dueCardsResponse{Cards: cards, NextDueAt: nextDueAt})
}

func (h *handler) submitReview(w http.ResponseWriter, r *http.Request) {
	cardID := r.PathValue("card_id")
	if _, err := uuid.Parse(cardID); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "card id must be a valid UUID"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var request reviewRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body must not exceed 1 MiB"})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body must contain valid JSON with an answer field"})
		return
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body must contain exactly one JSON object"})
		return
	}

	answer := strings.TrimSpace(request.Answer)
	if answer == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "answer must not be empty"})
		return
	}
	if utf8.RuneCountInString(answer) > maxReviewAnswerCharacters {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "answer must not exceed 2000 characters"})
		return
	}

	account, _ := userFromContext(r.Context())
	card, err := h.store.reviewCard(r.Context(), cardID, account.ID)
	if errors.Is(err, errCardNotFound) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "card not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not load card"})
		return
	}

	gradingStarted := time.Now()
	grade, err := h.generator.GradeAnswer(r.Context(), card.Question, card.ExpectedAnswer, card.Passage, answer)
	gradingDuration := time.Since(gradingStarted).Milliseconds()
	if errors.Is(err, generation.ErrInvalidGrade) {
		h.log(r.Context()).Error("grading rejected", "card_id", cardID, "duration_ms", gradingDuration, "error", err)
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "grader returned an invalid grade"})
		return
	}
	if err != nil {
		h.log(r.Context()).Error("grading failed", "card_id", cardID, "duration_ms", gradingDuration, "error", err)
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "could not grade the answer"})
		return
	}

	h.log(r.Context()).Info("answer graded", "card_id", cardID, "score", grade.Score, "duration_ms", gradingDuration)

	schedule, dueAt := scheduling.Next(card.Schedule, grade.Score, time.Now())

	if !account.IsDemo {
		err = h.store.recordReview(r.Context(), newReview{
			CardID:            cardID,
			Answer:            answer,
			Grade:             grade,
			Schedule:          schedule,
			DueAt:             dueAt,
			PreviousUpdatedAt: card.UpdatedAt,
		})
		if errors.Is(err, errReviewConflict) {
			h.log(r.Context()).Warn("review conflict", "card_id", cardID)
			writeJSON(w, http.StatusConflict, errorResponse{Error: "this card was already answered; reload the review queue"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not record the review"})
			return
		}
	}

	response := reviewResponse{
		Score:          grade.Score,
		Label:          gradeLabel(grade.Score),
		Rationale:      grade.Rationale,
		ExpectedAnswer: card.ExpectedAnswer,
		IntervalDays:   schedule.IntervalDays,
		NextDueAt:      dueAt,
	}
	if source, err := buildPassageContext(card.Location); err == nil {
		response.Source = &source
	}

	writeJSON(w, http.StatusOK, response)
}

func (s *PostgresStore) dueCards(ctx context.Context, limit, newCardCap int, userID string) ([]dueCard, error) {
	const reviewQuery = `
		SELECT k.id::text, k.question, COALESCE(d.title, ''), c.page_number
		FROM card_schedule s
		JOIN cards k ON k.id = s.card_id
		JOIN document_chunks c ON c.id = k.chunk_id
		JOIN documents d ON d.id = c.document_id
		WHERE s.due_at <= now()
		    AND d.user_id = $2
		    AND EXISTS (SELECT 1 FROM reviews r WHERE r.card_id = k.id)
		ORDER BY s.due_at, k.id
		LIMIT $1
	`
	cards, err := s.queryDueCards(ctx, reviewQuery, limit, userID, false)
	if err != nil {
		return nil, err
	}

	const introducedQuery = `
		SELECT count(*)
		FROM (
			SELECT r.card_id
			FROM reviews r
			JOIN cards k ON k.id = r.card_id
			JOIN document_chunks c ON c.id = k.chunk_id
			JOIN documents d ON d.id = c.document_id
			WHERE d.user_id = $1
			GROUP BY r.card_id
			HAVING min(r.reviewed_at) > now() - interval '24 hours'
		) introduced
	`
	var introduced int
	if err := s.pool.QueryRow(ctx, introducedQuery, userID).Scan(&introduced); err != nil {
		return nil, err
	}

	newLimit := min(limit-len(cards), newCardCap-introduced)
	if newLimit <= 0 {
		return cards, nil
	}

	const newQuery = `
		SELECT k.id::text, k.question, COALESCE(d.title, ''), c.page_number
		FROM card_schedule s
		JOIN cards k ON k.id = s.card_id
		JOIN document_chunks c ON c.id = k.chunk_id
		JOIN documents d ON d.id = c.document_id
		WHERE s.due_at <= now()
		    AND d.user_id = $2
		    AND NOT EXISTS (SELECT 1 FROM reviews r WHERE r.card_id = k.id)
		ORDER BY s.due_at, c.chunk_index, k.id
		LIMIT $1
	`
	newCards, err := s.queryDueCards(ctx, newQuery, newLimit, userID, true)
	if err != nil {
		return nil, err
	}

	return append(cards, newCards...), nil
}

func (s *PostgresStore) queryDueCards(ctx context.Context, query string, limit int, userID string, isNew bool) ([]dueCard, error) {
	rows, err := s.pool.Query(ctx, query, limit, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cards []dueCard
	for rows.Next() {
		card := dueCard{IsNew: isNew}
		if err := rows.Scan(&card.CardID, &card.Question, &card.Title, &card.Page); err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return cards, nil
}

func (s *PostgresStore) nextDueAt(ctx context.Context, userID string, newCardCap int) (*time.Time, error) {
	const query = `
		WITH introduced AS (
			SELECT min(r.reviewed_at) AS first_reviewed_at
			FROM reviews r
			JOIN cards k ON k.id = r.card_id
			JOIN document_chunks c ON c.id = k.chunk_id
			JOIN documents d ON d.id = c.document_id
			WHERE d.user_id = $1
			GROUP BY r.card_id
			HAVING min(r.reviewed_at) > now() - interval '24 hours'
		),
		scheduled AS (
			SELECT min(s.due_at) AS due_at
			FROM card_schedule s
			JOIN cards k ON k.id = s.card_id
			JOIN document_chunks c ON c.id = k.chunk_id
			JOIN documents d ON d.id = c.document_id
			WHERE s.due_at > now() AND d.user_id = $1
		),
		withheld AS (
			SELECT count(*) AS waiting
			FROM card_schedule s
			JOIN cards k ON k.id = s.card_id
			JOIN document_chunks c ON c.id = k.chunk_id
			JOIN documents d ON d.id = c.document_id
			WHERE d.user_id = $1
			    AND s.due_at <= now()
			    AND NOT EXISTS (SELECT 1 FROM reviews r WHERE r.card_id = k.id)
		)
		SELECT least(
			(SELECT due_at FROM scheduled),
			CASE
				WHEN (SELECT waiting FROM withheld) > 0
					AND (SELECT count(*) FROM introduced) >= $2
				THEN (SELECT min(first_reviewed_at) FROM introduced) + interval '24 hours'
			END
		)
	`

	var next *time.Time
	if err := s.pool.QueryRow(ctx, query, userID, newCardCap).Scan(&next); err != nil {
		return nil, err
	}

	return next, nil
}

func (s *PostgresStore) reviewCard(ctx context.Context, cardID, userID string) (reviewCard, error) {
	const query = `
		SELECT k.question, k.expected_answer, c.content,
			c.document_id::text, COALESCE(d.title, ''), d.source_type, d.content,
			c.page_number, c.start_offset, c.end_offset,
			s.repetitions, s.interval_days, s.ease_factor, s.lapses, s.updated_at
		FROM cards k
		JOIN card_schedule s ON s.card_id = k.id
		JOIN document_chunks c ON c.id = k.chunk_id
		JOIN documents d ON d.id = c.document_id
		WHERE k.id = $1 AND d.user_id = $2
	`

	var card reviewCard
	err := s.pool.QueryRow(ctx, query, cardID, userID).Scan(
		&card.Question,
		&card.ExpectedAnswer,
		&card.Passage,
		&card.Location.DocumentID,
		&card.Location.Title,
		&card.Location.SourceType,
		&card.Location.Content,
		&card.Location.Page,
		&card.Location.Start,
		&card.Location.End,
		&card.Schedule.Repetitions,
		&card.Schedule.IntervalDays,
		&card.Schedule.EaseFactor,
		&card.Schedule.Lapses,
		&card.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return reviewCard{}, errCardNotFound
	}
	if err != nil {
		return reviewCard{}, err
	}

	return card, nil
}

func (s *PostgresStore) recordReview(ctx context.Context, review newReview) error {
	transaction, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	const updateScheduleQuery = `
		UPDATE card_schedule
		SET due_at = $2,
		    interval_days = $3,
		    ease_factor = $4,
		    repetitions = $5,
		    lapses = $6,
		    updated_at = now()
		WHERE card_id = $1
		    AND updated_at = $7
	`
	result, err := transaction.Exec(ctx, updateScheduleQuery,
		review.CardID,
		review.DueAt,
		review.Schedule.IntervalDays,
		review.Schedule.EaseFactor,
		review.Schedule.Repetitions,
		review.Schedule.Lapses,
		review.PreviousUpdatedAt,
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errReviewConflict
	}

	const insertReviewQuery = `
		INSERT INTO reviews (card_id, user_answer, grade, rationale)
		VALUES ($1, $2, $3, $4)
	`
	if _, err := transaction.Exec(ctx, insertReviewQuery, review.CardID, review.Answer, review.Grade.Score, review.Grade.Rationale); err != nil {
		return err
	}

	return transaction.Commit(ctx)
}
