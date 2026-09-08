package api

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) createDocument(ctx context.Context, content string, chunks []documentChunk) (string, error) {
	transaction, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	const insertDocumentQuery = `
		INSERT INTO documents (id, content)
		VALUES (gen_random_uuid(), $1)
		RETURNING id::text
	`

	var id string
	if err := transaction.QueryRow(ctx, insertDocumentQuery, content).Scan(&id); err != nil {
		return "", err
	}

	const insertChunkQuery = `
		INSERT INTO document_chunks (document_id, chunk_index, content, embedding, embedding_model, embedded_at)
		VALUES ($1, $2, $3, $4::vector, $5, now())
	`
	for index, chunk := range chunks {
		if _, err := transaction.Exec(
			ctx,
			insertChunkQuery,
			id,
			index,
			chunk.Content,
			formatVector(chunk.Embedding),
			chunk.EmbeddingModel,
		); err != nil {
			return "", err
		}
	}

	if err := transaction.Commit(ctx); err != nil {
		return "", err
	}

	return id, nil
}

func formatVector(values []float32) string {
	var result strings.Builder
	result.WriteByte('[')
	for index, value := range values {
		if index > 0 {
			result.WriteByte(',')
		}
		result.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	result.WriteByte(']')
	return result.String()
}

func (s *PostgresStore) getDocument(ctx context.Context, id string) (document, error) {
	const query = `
		SELECT id::text, content, created_at
		FROM documents
		WHERE id = $1
	`

	var result document
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&result.ID,
		&result.Content,
		&result.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return document{}, errDocumentNotFound
	}
	if err != nil {
		return document{}, err
	}

	return result, nil
}
