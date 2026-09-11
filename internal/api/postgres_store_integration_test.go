package api

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/thenujawijesuriya/recall/internal/chunking"
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

	doc := newDocument{SourceType: sourceText, Content: rollbackDocumentContent}
	chunks := []chunking.Chunk{
		{Text: rollbackChunkContent, Start: 0, End: 10, Page: 1},
		{Text: "", Start: 10, End: 11, Page: 1},
	}

	if _, _, err := store.createDocument(ctx, doc, chunks); err == nil {
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

type storedSpan struct {
	content string
	page    *int
	start   *int
	end     *int
}

func storedSpans(t *testing.T, pool *pgxpool.Pool, documentID string) []storedSpan {
	t.Helper()

	rows, err := pool.Query(context.Background(),
		`SELECT content, page_number, start_offset, end_offset
		 FROM document_chunks
		 WHERE document_id = $1
		 ORDER BY chunk_index`,
		documentID,
	)
	if err != nil {
		t.Fatalf("query spans: %v", err)
	}
	defer rows.Close()

	var spans []storedSpan
	for rows.Next() {
		var span storedSpan
		if err := rows.Scan(&span.content, &span.page, &span.start, &span.end); err != nil {
			t.Fatalf("scan span: %v", err)
		}
		spans = append(spans, span)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read spans: %v", err)
	}

	return spans
}

func createTestDocument(t *testing.T, store *PostgresStore, pool *pgxpool.Pool, doc newDocument) string {
	t.Helper()
	ctx := context.Background()

	splitter, err := chunking.NewWordSplitter(4, 1)
	if err != nil {
		t.Fatalf("create splitter: %v", err)
	}

	id, _, err := store.createDocument(ctx, doc, splitter.Split(doc.Content))
	if err != nil {
		t.Fatalf("createDocument: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM documents WHERE id = $1`, id); err != nil {
			t.Errorf("clean up document: %v", err)
		}
	})

	return id
}

func TestPostgresCreateDocumentStoresPDFSpansAndPages(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)

	id := createTestDocument(t, store, pool, newDocument{
		Title:      "Lecture 3.pdf",
		SourceType: sourcePDF,
		Content:    "one two\fthree four",
	})

	stored, err := store.getDocument(context.Background(), id)
	if err != nil {
		t.Fatalf("getDocument: %v", err)
	}
	if stored.Title != "Lecture 3.pdf" || stored.SourceType != sourcePDF {
		t.Fatalf("title, source type = %q, %q; want %q, %q", stored.Title, stored.SourceType, "Lecture 3.pdf", sourcePDF)
	}

	spans := storedSpans(t, pool, id)
	want := []struct {
		content          string
		page, start, end int
	}{
		{"one two", 1, 0, 7},
		{"three four", 2, 8, 18},
	}
	if len(spans) != len(want) {
		t.Fatalf("spans = %d, want %d", len(spans), len(want))
	}
	for index, expected := range want {
		got := spans[index]
		if got.content != expected.content || got.page == nil || got.start == nil || got.end == nil ||
			*got.page != expected.page || *got.start != expected.start || *got.end != expected.end {
			t.Errorf("span %d = %q page=%v start=%v end=%v, want %+v", index, got.content, got.page, got.start, got.end, expected)
		}
	}
}

func TestPostgresCreateDocumentStoresTextWithoutPages(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)

	id := createTestDocument(t, store, pool, newDocument{
		SourceType: sourceText,
		Content:    "plain text document",
	})

	stored, err := store.getDocument(context.Background(), id)
	if err != nil {
		t.Fatalf("getDocument: %v", err)
	}
	if stored.Title != "" || stored.SourceType != sourceText {
		t.Fatalf("title, source type = %q, %q; want empty, %q", stored.Title, stored.SourceType, sourceText)
	}

	spans := storedSpans(t, pool, id)
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if spans[0].page != nil {
		t.Errorf("page_number = %d, want NULL for a text document", *spans[0].page)
	}
	if spans[0].start == nil || spans[0].end == nil || *spans[0].start != 0 || *spans[0].end != 19 {
		t.Errorf("offsets = %v..%v, want 0..19", spans[0].start, spans[0].end)
	}
}

func TestPostgresDocumentChunksRejectEmptySpan(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	const content = "empty span integration document, must not survive"
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM documents WHERE content = $1`, content); err != nil {
			t.Errorf("clean up empty span document: %v", err)
		}
	})

	doc := newDocument{SourceType: sourceText, Content: content}
	chunks := []chunking.Chunk{{Text: "empty", Start: 5, End: 5, Page: 1}}

	if _, _, err := store.createDocument(ctx, doc, chunks); err == nil {
		t.Fatal("createDocument() error = nil, want the span check to reject end_offset = start_offset")
	}
}
