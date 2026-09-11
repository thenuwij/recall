package api

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/thenujawijesuriya/recall/internal/chunking"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) createDocument(ctx context.Context, doc newDocument, chunks []chunking.Chunk) (string, string, error) {
	transaction, err := s.pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer func() {
		_ = transaction.Rollback(ctx)
	}()

	const insertDocumentQuery = `
		INSERT INTO documents (id, title, source_type, content)
		VALUES (gen_random_uuid(), NULLIF($1, ''), $2, $3)
		RETURNING id::text
	`

	var id string
	if err := transaction.QueryRow(ctx, insertDocumentQuery, doc.Title, doc.SourceType, doc.Content).Scan(&id); err != nil {
		return "", "", err
	}

	const insertChunkQuery = `
		INSERT INTO document_chunks (document_id, chunk_index, content, page_number, start_offset, end_offset)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	for index, chunk := range chunks {
		var page *int
		if doc.SourceType == sourcePDF {
			page = &chunk.Page
		}
		if _, err := transaction.Exec(ctx, insertChunkQuery, id, index, chunk.Text, page, chunk.Start, chunk.End); err != nil {
			return "", "", err
		}
	}

	const insertJobQuery = `
		INSERT INTO ingestion_jobs (document_id)
		VALUES ($1)
		RETURNING id::text
	`
	var jobID string
	if err := transaction.QueryRow(ctx, insertJobQuery, id).Scan(&jobID); err != nil {
		return "", "", err
	}

	if err := transaction.Commit(ctx); err != nil {
		return "", "", err
	}

	return id, jobID, nil
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
		SELECT d.id::text, COALESCE(d.title, ''), d.source_type, d.content, d.created_at, j.state, j.last_error
		FROM documents d
		LEFT JOIN ingestion_jobs j ON j.document_id = d.id
		WHERE d.id = $1
	`

	var result document
	var state, lastError *string
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&result.ID,
		&result.Title,
		&result.SourceType,
		&result.Content,
		&result.CreatedAt,
		&state,
		&lastError,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return document{}, errDocumentNotFound
	}
	if err != nil {
		return document{}, err
	}

	result.Status = ingestionStatus(state)
	if result.Status == statusFailed && lastError != nil {
		result.Reason = *lastError
	}

	return result, nil
}

func (s *PostgresStore) searchChunks(ctx context.Context, queryEmbedding []float32, model string, limit int) ([]searchResult, error) {
	const query = `
		SELECT
			id::text,
			document_id::text,
			chunk_index,
			content,
			1 - (embedding <=> $1::vector) AS similarity
		FROM document_chunks
		WHERE embedding IS NOT NULL
			AND embedding_model = $2
		ORDER BY embedding <=> $1::vector, document_id, chunk_index
		LIMIT $3
	`

	rows, err := s.pool.Query(ctx, query, formatVector(queryEmbedding), model, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]searchResult, 0, limit)
	for rows.Next() {
		var result searchResult
		if err := rows.Scan(
			&result.ChunkID,
			&result.DocumentID,
			&result.ChunkIndex,
			&result.Content,
			&result.Similarity,
		); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}
