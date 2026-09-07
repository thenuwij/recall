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

func (s *PostgresStore) createDocument(ctx context.Context, content string) (string, error) {
	const query = `
		INSERT INTO documents (id, content)
		VALUES (gen_random_uuid(), $1)
		RETURNING id::text
	`

	var id string
	if err := s.pool.QueryRow(ctx, query, content).Scan(&id); err != nil {
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
