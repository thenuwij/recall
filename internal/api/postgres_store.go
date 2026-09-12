package api

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
		INSERT INTO documents (id, title, source_type, content, user_id)
		VALUES (gen_random_uuid(), NULLIF($1, ''), $2, $3, $4)
		RETURNING id::text
	`

	var id string
	if err := transaction.QueryRow(ctx, insertDocumentQuery, doc.Title, doc.SourceType, doc.Content, doc.UserID).Scan(&id); err != nil {
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

func (s *PostgresStore) getDocument(ctx context.Context, id, userID string) (document, error) {
	const query = `
		SELECT d.id::text, COALESCE(d.title, ''), d.source_type, d.content, d.created_at, e.state, e.last_error, c.state,
			(
				SELECT count(*)
				FROM cards
				JOIN document_chunks ON document_chunks.id = cards.chunk_id
				WHERE document_chunks.document_id = d.id
			)
		FROM documents d
		LEFT JOIN ingestion_jobs e ON e.document_id = d.id AND e.kind = $2
		LEFT JOIN ingestion_jobs c ON c.document_id = d.id AND c.kind = $3
		WHERE d.id = $1 AND d.user_id = $4
	`

	var result document
	var state, lastError, cardState *string
	err := s.pool.QueryRow(ctx, query, id, jobKindEmbed, jobKindCards, userID).Scan(
		&result.ID,
		&result.Title,
		&result.SourceType,
		&result.Content,
		&result.CreatedAt,
		&state,
		&lastError,
		&cardState,
		&result.CardCount,
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
	result.CardsStatus = cardsStatus(cardState)

	return result, nil
}

func (s *PostgresStore) searchChunks(ctx context.Context, queryEmbedding []float32, model string, limit int, userID string) ([]searchResult, error) {
	const query = `
		SELECT
			c.id::text,
			c.document_id::text,
			COALESCE(d.title, ''),
			c.chunk_index,
			c.page_number,
			c.content,
			1 - (c.embedding <=> $1::vector) AS similarity
		FROM document_chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE c.embedding IS NOT NULL
			AND c.embedding_model = $2
			AND d.user_id = $4
		ORDER BY c.embedding <=> $1::vector, c.document_id, c.chunk_index
		LIMIT $3
	`

	rows, err := s.pool.Query(ctx, query, formatVector(queryEmbedding), model, limit, userID)
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
			&result.Title,
			&result.ChunkIndex,
			&result.Page,
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

func (s *PostgresStore) listDocuments(ctx context.Context, userID string) ([]documentSummary, error) {
	const query = `
		SELECT d.id::text, COALESCE(d.title, ''), d.source_type, d.created_at, e.state, c.state,
			(
				SELECT count(*)
				FROM cards
				JOIN document_chunks ON document_chunks.id = cards.chunk_id
				WHERE document_chunks.document_id = d.id
			)
		FROM documents d
		LEFT JOIN ingestion_jobs e ON e.document_id = d.id AND e.kind = $1
		LEFT JOIN ingestion_jobs c ON c.document_id = d.id AND c.kind = $2
		WHERE d.user_id = $3
		ORDER BY d.created_at DESC, d.id
	`

	rows, err := s.pool.Query(ctx, query, jobKindEmbed, jobKindCards, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var documents []documentSummary
	for rows.Next() {
		var summary documentSummary
		var state, cardState *string
		if err := rows.Scan(&summary.ID, &summary.Title, &summary.SourceType, &summary.CreatedAt, &state, &cardState, &summary.CardCount); err != nil {
			return nil, err
		}
		summary.Status = ingestionStatus(state)
		summary.CardsStatus = cardsStatus(cardState)
		documents = append(documents, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return documents, nil
}

func (s *PostgresStore) deleteDocument(ctx context.Context, id, userID string) error {
	result, err := s.pool.Exec(ctx, `DELETE FROM documents WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errDocumentNotFound
	}

	return nil
}

func (s *PostgresStore) chunkLocation(ctx context.Context, chunkID, userID string) (chunkLocation, error) {
	const query = `
		SELECT c.document_id::text, COALESCE(d.title, ''), d.source_type, d.content,
			c.page_number, c.start_offset, c.end_offset
		FROM document_chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE c.id = $1 AND d.user_id = $2
	`

	var location chunkLocation
	err := s.pool.QueryRow(ctx, query, chunkID, userID).Scan(
		&location.DocumentID,
		&location.Title,
		&location.SourceType,
		&location.Content,
		&location.Page,
		&location.Start,
		&location.End,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return chunkLocation{}, errChunkNotFound
	}
	if err != nil {
		return chunkLocation{}, err
	}

	return location, nil
}

func (s *PostgresStore) createUser(ctx context.Context, email, passwordHash string) (user, error) {
	const query = `
		INSERT INTO users (email, password_hash)
		VALUES ($1, $2)
		RETURNING id::text, email, max_documents, max_pages_per_document
	`

	var created user
	err := s.pool.QueryRow(ctx, query, email, passwordHash).Scan(
		&created.ID, &created.Email, &created.MaxDocuments, &created.MaxPagesPerDocument,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return user{}, errEmailTaken
		}
		return user{}, err
	}

	return created, nil
}

func (s *PostgresStore) userByEmail(ctx context.Context, email string) (user, string, error) {
	const query = `
		SELECT id::text, email, max_documents, max_pages_per_document, password_hash
		FROM users
		WHERE email = $1
	`

	var found user
	var hash string
	err := s.pool.QueryRow(ctx, query, email).Scan(
		&found.ID, &found.Email, &found.MaxDocuments, &found.MaxPagesPerDocument, &hash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return user{}, "", errUserNotFound
	}
	if err != nil {
		return user{}, "", err
	}

	return found, hash, nil
}

func (s *PostgresStore) createSession(ctx context.Context, token, userID string, expiresAt time.Time) error {
	const query = `
		INSERT INTO sessions (token, user_id, expires_at)
		VALUES ($1, $2, $3)
	`

	_, err := s.pool.Exec(ctx, query, token, userID, expiresAt)
	return err
}

func (s *PostgresStore) userBySession(ctx context.Context, token string) (user, error) {
	const query = `
		SELECT users.id::text, users.email, users.max_documents, users.max_pages_per_document
		FROM sessions
		JOIN users ON users.id = sessions.user_id
		WHERE sessions.token = $1 AND sessions.expires_at > now()
	`

	var found user
	err := s.pool.QueryRow(ctx, query, token).Scan(
		&found.ID, &found.Email, &found.MaxDocuments, &found.MaxPagesPerDocument,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return user{}, errSessionNotFound
	}
	if err != nil {
		return user{}, err
	}

	return found, nil
}

func (s *PostgresStore) deleteSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, token)
	return err
}
