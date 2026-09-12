package api

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/thenujawijesuriya/recall/internal/chunking"
)

const testOwnerID = "00000000-0000-0000-0000-00000000f00d"

func ensureTestOwner(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	const query = `
		INSERT INTO users (id, email, password_hash)
		VALUES ($1, 'fixtures@example.com', 'fixture-hash')
		ON CONFLICT (id) DO NOTHING
	`
	if _, err := pool.Exec(context.Background(), query, testOwnerID); err != nil {
		t.Fatalf("create fixture owner: %v", err)
	}
}

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

	ensureTestOwner(t, pool)

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
		`INSERT INTO documents (id, content, user_id) VALUES ($1, 'retrieval integration fixture', $2)`,
		fixtureDocumentID, testOwnerID,
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

	results, err := store.searchChunks(context.Background(), testVector(1, 0), "text-embedding-3-small", 20, testOwnerID)
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

	results, err := store.searchChunks(context.Background(), testVector(1, 0), "text-embedding-3-small", 20, testOwnerID)
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

	results, err := store.searchChunks(context.Background(), testVector(1, 0), "text-embedding-3-small", 2, testOwnerID)
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

	if _, err := store.searchChunks(ctx, testVector(1, 0), "text-embedding-3-small", 5, testOwnerID); err == nil {
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

	doc.UserID = testOwnerID
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

	stored, err := store.getDocument(context.Background(), id, testOwnerID)
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

	stored, err := store.getDocument(context.Background(), id, testOwnerID)
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

func TestPostgresListDocumentsIncludesStatusAndTitle(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)

	id := createTestDocument(t, store, pool, newDocument{Title: "Listed", SourceType: sourcePDF, Content: "listed document"})

	documents, err := store.listDocuments(context.Background(), testOwnerID)
	if err != nil {
		t.Fatalf("listDocuments: %v", err)
	}
	for _, summary := range documents {
		if summary.ID == id {
			if summary.Title != "Listed" || summary.SourceType != sourcePDF || summary.Status != statusQueued {
				t.Fatalf("summary = %+v", summary)
			}
			return
		}
	}
	t.Fatalf("created document %s not listed", id)
}

func TestPostgresDocumentStatusSeparatesEmbedAndCardJobs(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	id := createTestDocument(t, store, pool, newDocument{Title: "Two jobs", SourceType: sourceText, Content: "document with two jobs"})

	stored, err := store.getDocument(ctx, id, testOwnerID)
	if err != nil {
		t.Fatalf("getDocument: %v", err)
	}
	if stored.Status != statusQueued || stored.CardsStatus != statusNotStarted {
		t.Fatalf("status = %q, cards_status = %q, want %q and %q", stored.Status, stored.CardsStatus, statusQueued, statusNotStarted)
	}

	var jobID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM ingestion_jobs WHERE document_id = $1`, id).Scan(&jobID); err != nil {
		t.Fatalf("read job id: %v", err)
	}
	if err := store.completeIngestionJob(ctx, jobID, nil, ""); err != nil {
		t.Fatalf("completeIngestionJob: %v", err)
	}

	stored, err = store.getDocument(ctx, id, testOwnerID)
	if err != nil {
		t.Fatalf("getDocument after completion: %v", err)
	}
	if stored.Status != statusReady || stored.CardsStatus != statusQueued {
		t.Fatalf("status = %q, cards_status = %q, want %q and %q", stored.Status, stored.CardsStatus, statusReady, statusQueued)
	}

	documents, err := store.listDocuments(ctx, testOwnerID)
	if err != nil {
		t.Fatalf("listDocuments: %v", err)
	}
	listed := 0
	for _, summary := range documents {
		if summary.ID != id {
			continue
		}
		listed++
		if summary.Status != statusReady || summary.CardsStatus != statusQueued {
			t.Errorf("summary = %+v, want status %q and cards_status %q", summary, statusReady, statusQueued)
		}
	}
	if listed != 1 {
		t.Fatalf("document listed %d times, want once", listed)
	}
}

func TestPostgresDeleteDocumentCascades(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	id := createTestDocument(t, store, pool, newDocument{SourceType: sourceText, Content: "document to delete"})

	if err := store.deleteDocument(ctx, id, testOwnerID); err != nil {
		t.Fatalf("deleteDocument: %v", err)
	}

	var chunks, jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM document_chunks WHERE document_id = $1`, id).Scan(&chunks); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ingestion_jobs WHERE document_id = $1`, id).Scan(&jobs); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if chunks != 0 || jobs != 0 {
		t.Fatalf("after delete: chunks = %d, jobs = %d, want 0 and 0", chunks, jobs)
	}

	if err := store.deleteDocument(ctx, id, testOwnerID); !errors.Is(err, errDocumentNotFound) {
		t.Fatalf("second delete error = %v, want %v", err, errDocumentNotFound)
	}
}

func TestPostgresChunkLocationAndSearchIncludeTitleAndPage(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	id := createTestDocument(t, store, pool, newDocument{Title: "Lecture 3", SourceType: sourcePDF, Content: "first page\fsecond page"})

	var chunkID string
	if err := pool.QueryRow(ctx,
		`UPDATE document_chunks SET embedding = $2::vector, embedding_model = 'test-location-model', embedded_at = now()
		 WHERE document_id = $1 AND page_number = 2
		 RETURNING id::text`,
		id, formatVector(testVector(1, 0)),
	).Scan(&chunkID); err != nil {
		t.Fatalf("embed page two chunk: %v", err)
	}

	location, err := store.chunkLocation(ctx, chunkID, testOwnerID)
	if err != nil {
		t.Fatalf("chunkLocation: %v", err)
	}
	if location.DocumentID != id || location.Title != "Lecture 3" || location.Page == nil || *location.Page != 2 ||
		location.Start == nil || *location.Start != 11 || location.End == nil || *location.End != 22 {
		t.Fatalf("location = %+v", location)
	}

	results, err := store.searchChunks(ctx, testVector(1, 0), "test-location-model", 5, testOwnerID)
	if err != nil {
		t.Fatalf("searchChunks: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Lecture 3" || results[0].Page == nil || *results[0].Page != 2 {
		t.Fatalf("results = %+v, want one result titled Lecture 3 on page 2", results)
	}

	if _, err := store.chunkLocation(ctx, "00000000-0000-0000-0000-000000000000", testOwnerID); !errors.Is(err, errChunkNotFound) {
		t.Fatalf("unknown chunk error = %v, want %v", err, errChunkNotFound)
	}
}
