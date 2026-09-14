package api

import (
	"context"
	"errors"
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
