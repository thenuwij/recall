package api

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseURL := os.Getenv("RECALL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set RECALL_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}

	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping test database: %v", err)
	}

	return pool
}

func testVector(a, b float32) []float32 {
	vector := make([]float32, 1536)
	vector[0] = a
	vector[1] = b
	return vector
}

const fixtureDocumentID = "00000000-0000-0000-0000-0000000000fe"

func insertFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM documents WHERE id = $1`, fixtureDocumentID); err != nil {
			t.Errorf("clean up fixture: %v", err)
		}
	})

	if _, err := pool.Exec(ctx,
		`INSERT INTO documents (id, content) VALUES ($1, 'retrieval integration fixture')`,
		fixtureDocumentID,
	); err != nil {
		t.Fatalf("insert fixture document: %v", err)
	}

	rows := []struct {
		index     int
		content   string
		embedding []float32
		model     string
	}{
		{0, "A exact match", testVector(1, 0), "text-embedding-3-small"},
		{1, "B partial match", testVector(1, 1), "text-embedding-3-small"},
		{2, "C unrelated", testVector(0, 1), "text-embedding-3-small"},
		{4, "E wrong model", testVector(1, 0), "text-embedding-3-large"},
	}
	for _, row := range rows {
		if _, err := pool.Exec(ctx,
			`INSERT INTO document_chunks (document_id, chunk_index, content, embedding, embedding_model, embedded_at)
			 VALUES ($1, $2, $3, $4::vector, $5, now())`,
			fixtureDocumentID, row.index, row.content, formatVector(row.embedding), row.model,
		); err != nil {
			t.Fatalf("insert fixture chunk %d: %v", row.index, err)
		}
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO document_chunks (document_id, chunk_index, content) VALUES ($1, 3, 'D null embedding')`,
		fixtureDocumentID,
	); err != nil {
		t.Fatalf("insert null-embedding chunk: %v", err)
	}
}

func TestPostgresSearchChunksRanksByCosineSimilarity(t *testing.T) {
	pool := testPool(t)
	insertFixture(t, pool)
	store := NewPostgresStore(pool)

	results, err := store.searchChunks(context.Background(), testVector(1, 0), "text-embedding-3-small", 20)
	if err != nil {
		t.Fatalf("searchChunks: %v", err)
	}

	var fixture []searchResult
	for _, result := range results {
		if result.DocumentID == fixtureDocumentID {
			fixture = append(fixture, result)
		}
	}

	want := []struct {
		content    string
		similarity float64
	}{
		{"A exact match", 1.0},
		{"B partial match", 0.7071067811865475},
		{"C unrelated", 0.0},
	}

	if len(fixture) != len(want) {
		t.Fatalf("fixture results = %d, want %d: %+v", len(fixture), len(want), fixture)
	}
	for index, expected := range want {
		got := fixture[index]
		if got.Content != expected.content {
			t.Errorf("result %d content = %q, want %q", index, got.Content, expected.content)
		}
		if difference := got.Similarity - expected.similarity; difference > 1e-6 || difference < -1e-6 {
			t.Errorf("result %d similarity = %v, want %v", index, got.Similarity, expected.similarity)
		}
		if got.ChunkID == "" {
			t.Errorf("result %d has empty chunk id", index)
		}
	}
}

func TestPostgresSearchChunksExcludesNullAndOtherModels(t *testing.T) {
	pool := testPool(t)
	insertFixture(t, pool)
	store := NewPostgresStore(pool)

	results, err := store.searchChunks(context.Background(), testVector(1, 0), "text-embedding-3-small", 20)
	if err != nil {
		t.Fatalf("searchChunks: %v", err)
	}

	for _, result := range results {
		if result.Content == "E wrong model" {
			t.Errorf("result from another embedding model was returned: %+v", result)
		}
		if result.Content == "D null embedding" {
			t.Errorf("chunk without an embedding was returned: %+v", result)
		}
	}
}

func TestPostgresSearchChunksRespectsLimit(t *testing.T) {
	pool := testPool(t)
	insertFixture(t, pool)
	store := NewPostgresStore(pool)

	results, err := store.searchChunks(context.Background(), testVector(1, 0), "text-embedding-3-small", 2)
	if err != nil {
		t.Fatalf("searchChunks: %v", err)
	}
	if len(results) > 2 {
		t.Fatalf("results = %d, want at most 2", len(results))
	}
}

func TestPostgresSearchChunksHonoursCancelledContext(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.searchChunks(ctx, testVector(1, 0), "text-embedding-3-small", 5); err == nil {
		t.Fatal("searchChunks() error = nil, want an error for a cancelled context")
	}
}

func TestPostgresCreateDocumentRollsBackOnChunkFailure(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	const (
		rollbackDocumentContent = "rollback integration document, must not survive"
		rollbackChunkContent    = "rollback integration chunk, must not survive"
	)

	t.Cleanup(func() {
		if _, err := pool.Exec(ctx,
			`DELETE FROM documents WHERE content = $1`,
			rollbackDocumentContent,
		); err != nil {
			t.Errorf("clean up rollback document: %v", err)
		}
	})

	chunks := []string{rollbackChunkContent, ""}

	if _, _, err := store.createDocument(ctx, rollbackDocumentContent, chunks); err == nil {
		t.Fatal("createDocument() error = nil, want an error for a blank chunk")
	}

	var documents int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM documents WHERE content = $1`,
		rollbackDocumentContent,
	).Scan(&documents); err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if documents != 0 {
		t.Errorf("documents = %d, want 0: a failed chunk insert left the document behind", documents)
	}

	var chunkRows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM document_chunks WHERE content = $1`,
		rollbackChunkContent,
	).Scan(&chunkRows); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if chunkRows != 0 {
		t.Errorf("document_chunks = %d, want 0: the first chunk survived a failed transaction", chunkRows)
	}
}
