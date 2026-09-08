package api

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) createDocument(ctx context.Context, content string, chunks []string) (string, error) {
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
		INSERT INTO document_chunks (document_id, chunk_index, content)
		VALUES ($1, $2, $3)
	`
	for index, chunk := range chunks {
		if _, err := transaction.Exec(ctx, insertChunkQuery, id, index, chunk); err != nil {
			return "", err
		}
	}

	if err := transaction.Commit(ctx); err != nil {
		return "", err
	}

	return id, nil
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
