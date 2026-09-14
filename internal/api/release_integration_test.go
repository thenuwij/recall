package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPostgresStaleClaimCannotMutateReplacement(t *testing.T) {
	for _, kind := range []string{jobKindEmbed, jobKindCards} {
		t.Run(kind, func(t *testing.T) {
			pool := testPool(t)
			s := NewPostgresStore(pool)
			ctx := context.Background()
			id := createTestDocument(t, s, pool, newDocument{SourceType: sourceText, Content: "one two three four"})
			if kind == jobKindCards {
				claimedCardJob(t, s, pool, id)
				if _, err := pool.Exec(ctx, `UPDATE ingestion_jobs SET claimed_until=now()-interval '1 minute' WHERE document_id=$1 AND kind=$2`, id, kind); err != nil {
					t.Fatal(err)
				}
			}
			old, err := s.claimIngestionJob(ctx, -time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			current, err := s.claimIngestionJob(ctx, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if current.ID != old.ID || current.Attempts != old.Attempts+1 {
				t.Fatal("did not replace same job")
			}
			operations := []struct {
				name string
				run  func() error
			}{
				{"renew", func() error { return s.renewIngestionJob(ctx, old.ID, old.Attempts, time.Minute) }},
				{"retry", func() error { return s.failIngestionJob(ctx, old, "stale retry", false, 0) }},
				{"fail", func() error { return s.failIngestionJob(ctx, old, "stale failure", true, 0) }},
			}
			if kind == jobKindCards {
				operations = append(operations, struct {
					name string
					run  func() error
				}{"cards", func() error {
					return s.completeCardJob(ctx, old.ID, old.Attempts, []newCard{{ChunkID: firstChunkID(t, pool, id), Question: "stale", ExpectedAnswer: "stale"}})
				}})
			} else {
				operations = append(operations, struct {
					name string
					run  func() error
				}{"embeddings", func() error {
					return s.completeIngestionJob(ctx, old.ID, old.Attempts, []embeddedChunk{{ID: firstChunkID(t, pool, id), Embedding: testVector(1, 0)}}, "test")
				}})
			}
			for _, op := range operations {
				if err := op.run(); !errors.Is(err, errJobNoLongerHeld) {
					t.Fatalf("%s accepted stale claim: %v", op.name, err)
				}
			}
			var attempts int
			var state string
			if err := pool.QueryRow(ctx, `SELECT attempts,state FROM ingestion_jobs WHERE id=$1`, current.ID).Scan(&attempts, &state); err != nil {
				t.Fatal(err)
			}
			if attempts != current.Attempts || state != jobProcessing {
				t.Fatal("replacement was changed")
			}
			if got := countRows(t, pool, `SELECT count(*) FROM document_chunks WHERE document_id=$1 AND embedding IS NOT NULL`, id); got != 0 {
				t.Fatal("stale embeddings committed")
			}
			if got := countRows(t, pool, `SELECT count(*) FROM cards WHERE chunk_id IN(SELECT id FROM document_chunks WHERE document_id=$1)`, id); got != 0 {
				t.Fatal("stale cards committed")
			}
		})
	}
}
func TestPostgresAbandonedFinalAttemptIsTerminal(t *testing.T) {
	pool := testPool(t)
	s := NewPostgresStore(pool)
	ctx := context.Background()
	createTestDocument(t, s, pool, newDocument{SourceType: sourceText, Content: "one two three four"})
	for i := 0; i < maxIngestionAttempts; i++ {
		if _, err := s.claimIngestionJob(ctx, -time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.claimIngestionJob(ctx, time.Minute); !errors.Is(err, errNoIngestionJob) {
		t.Fatalf("exhausted job reclaimed: %v", err)
	}
	if countRows(t, pool, `SELECT count(*) FROM ingestion_jobs WHERE state='failed'`) != 1 {
		t.Fatal("final crash not marked failed")
	}
}
func TestPostgresFoldersScopeReviewsAndKeepDocuments(t *testing.T) {
	pool := testPool(t)
	s := NewPostgresStore(pool)
	ctx := context.Background()
	f, err := s.saveFolder(ctx, "", testOwnerID, "Systems")
	if err != nil {
		t.Fatal(err)
	}
	id := createTestDocument(t, s, pool, newDocument{SourceType: sourceText, Content: "one two three four"})
	other := createTestDocument(t, s, pool, newDocument{SourceType: sourceText, Content: "five six seven eight"})
	// Claim fixtures by id rather than relying on ordering across documents.
	for _, docID := range []string{id, other} {
		var jobID string
		if err := pool.QueryRow(ctx, `UPDATE ingestion_jobs SET state='processing',attempts=1 WHERE document_id=$1 RETURNING id::text`, docID).Scan(&jobID); err != nil {
			t.Fatal(err)
		}
		if err := s.completeIngestionJob(ctx, jobID, 1, nil, ""); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `UPDATE ingestion_jobs SET state='processing',attempts=1 WHERE document_id=$1 AND kind='generate_cards' RETURNING id::text`, docID).Scan(&jobID); err != nil {
			t.Fatal(err)
		}
		if err := s.completeCardJob(ctx, jobID, 1, []newCard{{ChunkID: firstChunkID(t, pool, docID), Question: docID, ExpectedAnswer: "answer"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.moveDocument(ctx, id, testOwnerID, f.ID); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []reviewScope{{FolderID: f.ID}, {DocumentID: id}} {
		cards, err := s.dueCards(ctx, 100, 20, testOwnerID, scope)
		if err != nil || len(cards) != 1 || cards[0].Question != id {
			t.Fatalf("scope leaked: %+v %v", cards, err)
		}
	}
	if err := s.moveDocument(ctx, id, testOwnerID, "00000000-0000-0000-0000-00000000abcd"); !errors.Is(err, errFolderNotFound) {
		t.Fatalf("unknown folder accepted: %v", err)
	}
	if _, err := s.saveFolder(ctx, f.ID, "00000000-0000-0000-0000-00000000abcd", "Intruder"); !errors.Is(err, errFolderNotFound) {
		t.Fatalf("cross-user rename: %v", err)
	}
	if err := s.deleteFolder(ctx, f.ID, testOwnerID); err != nil {
		t.Fatal(err)
	}
	docs, err := s.listDocuments(ctx, testOwnerID)
	if err != nil || len(docs) != 2 {
		t.Fatalf("documents lost: %v", err)
	}
	for _, doc := range docs {
		if doc.FolderID != "" {
			t.Fatal("folder not cleared")
		}
	}
}
func TestPostgresOriginalPDFIsPrivateAndSupportsRanges(t *testing.T) {
	pool := testPool(t)
	s := NewPostgresStore(pool)
	ctx := context.Background()
	data := []byte("%PDF-1.4\noriginal bytes\n%%EOF")
	id := createTestDocument(t, s, pool, newDocument{SourceType: sourcePDF, Content: "one two three four", PDF: data})
	got, err := s.originalPDF(ctx, id, testOwnerID)
	if err != nil || string(got) != string(data) {
		t.Fatalf("original changed: %v", err)
	}
	if _, err := s.originalPDF(ctx, id, "00000000-0000-0000-0000-00000000abcd"); !errors.Is(err, errDocumentNotFound) {
		t.Fatalf("cross-user PDF access: %v", err)
	}
	h := &handler{store: s}
	request := httptest.NewRequest(http.MethodGet, "/documents/"+id+"/pdf", nil)
	request.SetPathValue("id", id)
	request.Header.Set("Range", "bytes=0-7")
	request = request.WithContext(context.WithValue(request.Context(), userContextKey, user{ID: testOwnerID}))
	response := httptest.NewRecorder()
	h.pdf(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != string(data[:8]) {
		t.Fatalf("range failed: %d %q", response.Code, response.Body.String())
	}
	if err := s.deleteDocument(ctx, id, testOwnerID); err != nil {
		t.Fatal(err)
	}
	if countRows(t, pool, `SELECT count(*) FROM document_files`) != 0 {
		t.Fatal("orphaned PDF")
	}
}
