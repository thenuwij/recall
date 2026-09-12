package api

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/thenujawijesuriya/recall/internal/generation"
)

const (
	cardBatchSize           = 5
	duplicateCardSimilarity = 0.92
)

type cardGenerator interface {
	GenerateCards(ctx context.Context, passages []generation.Passage) ([]generation.GeneratedCard, error)
}

type newCard struct {
	ChunkID        string
	Question       string
	ExpectedAnswer string
}

func (w *Worker) processCards(ctx context.Context, job ingestionJob, started time.Time) error {
	chunks, err := w.store.documentChunks(ctx, job.DocumentID)
	if err != nil {
		return w.fail(ctx, job, fmt.Sprintf("read chunks: %v", err), false)
	}

	var generated []newCard
	for start := 0; start < len(chunks); start += cardBatchSize {
		batch := chunks[start:min(start+cardBatchSize, len(chunks))]

		passages := make([]generation.Passage, len(batch))
		for index, chunk := range batch {
			passages[index] = generation.Passage{Marker: index + 1, Content: chunk.Content}
		}

		cards, err := w.generator.GenerateCards(ctx, passages)
		if err != nil {
			return w.fail(ctx, job, fmt.Sprintf("generate cards: %v", err), permanentFailure(err))
		}
		for _, card := range cards {
			generated = append(generated, newCard{
				ChunkID:        batch[card.Marker-1].ID,
				Question:       card.Question,
				ExpectedAnswer: card.ExpectedAnswer,
			})
		}
	}

	kept := generated
	if len(generated) > 1 {
		questions := make([]string, len(generated))
		for index, card := range generated {
			questions[index] = card.Question
		}

		vectors, err := w.embedder.Embed(ctx, questions)
		if err != nil {
			return w.fail(ctx, job, fmt.Sprintf("embed questions: %v", err), permanentFailure(err))
		}
		if len(vectors) != len(generated) {
			return w.fail(ctx, job, fmt.Sprintf("provider returned %d vectors for %d questions", len(vectors), len(generated)), true)
		}

		kept = nil
		var keptVectors [][]float32
		for index, card := range generated {
			duplicate := false
			for _, keptVector := range keptVectors {
				if cosineSimilarity(vectors[index], keptVector) >= duplicateCardSimilarity {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			kept = append(kept, card)
			keptVectors = append(keptVectors, vectors[index])
		}
	}

	if err := w.store.completeCardJob(ctx, job.ID, kept); err != nil {
		return w.fail(ctx, job, fmt.Sprintf("store cards: %v", err), false)
	}

	w.log().Info("job completed",
		"job_id", job.ID,
		"kind", job.Kind,
		"document_id", job.DocumentID,
		"chunks", len(chunks),
		"cards_generated", len(generated),
		"cards_kept", len(kept),
		"duration_ms", time.Since(started).Milliseconds(),
	)
	return nil
}

func cosineSimilarity(a, b []float32) float64 {
	var dot, normA, normB float64
	for index := range a {
		dot += float64(a[index]) * float64(b[index])
		normA += float64(a[index]) * float64(a[index])
		normB += float64(b[index]) * float64(b[index])
	}
	if normA == 0 || normB == 0 {
		return 0
	}

	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func (s *PostgresStore) documentChunks(ctx context.Context, documentID string) ([]pendingChunk, error) {
	const query = `
		SELECT id::text, content
		FROM document_chunks
		WHERE document_id = $1
		ORDER BY chunk_index
	`

	rows, err := s.pool.Query(ctx, query, documentID)
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

func (s *PostgresStore) completeCardJob(ctx context.Context, jobID string, cards []newCard) error {
	transaction, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	const insertCardQuery = `
		INSERT INTO cards (chunk_id, question, expected_answer)
		VALUES ($1, $2, $3)
		RETURNING id::text
	`
	const insertScheduleQuery = `
		INSERT INTO card_schedule (card_id)
		VALUES ($1)
	`
	for _, card := range cards {
		var cardID string
		if err := transaction.QueryRow(ctx, insertCardQuery, card.ChunkID, card.Question, card.ExpectedAnswer).Scan(&cardID); err != nil {
			return err
		}
		if _, err := transaction.Exec(ctx, insertScheduleQuery, cardID); err != nil {
			return err
		}
	}

	const completeQuery = `
		UPDATE ingestion_jobs
		SET state = $2,
		    claimed_until = NULL,
		    last_error = NULL,
		    updated_at = now()
		WHERE id = $1 AND state = $3
	`
	result, err := transaction.Exec(ctx, completeQuery, jobID, jobCompleted, jobProcessing)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errJobNoLongerHeld
	}

	return transaction.Commit(ctx)
}
