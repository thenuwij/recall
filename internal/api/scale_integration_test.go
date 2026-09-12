package api

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/thenujawijesuriya/recall/internal/embedding"
)

func randomUnitVector(source *rand.Rand) []float32 {
	vector := make([]float32, embedding.Dimensions)
	var norm float64
	for index := range vector {
		value := source.NormFloat64()
		vector[index] = float32(value)
		norm += value * value
	}

	norm = math.Sqrt(norm)
	for index := range vector {
		vector[index] = float32(float64(vector[index]) / norm)
	}

	return vector
}

func seedScaleCorpus(t *testing.T, pool *pgxpool.Pool, documents, chunksPer int) string {
	t.Helper()
	ctx := context.Background()
	source := rand.New(rand.NewSource(1))

	var documentID string
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM documents WHERE title = 'scale fixture'`); err != nil {
			t.Errorf("clean up scale corpus: %v", err)
		}
	})

	started := time.Now()
	for document := range documents {
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO documents (id, title, content, user_id) VALUES (gen_random_uuid(), 'scale fixture', $1, $2) RETURNING id::text`,
			fmt.Sprintf("scale document %d", document), testOwnerID,
		).Scan(&id); err != nil {
			t.Fatalf("insert scale document: %v", err)
		}
		documentID = id

		batch := &pgx.Batch{}
		for chunk := range chunksPer {
			batch.Queue(
				`INSERT INTO document_chunks (document_id, chunk_index, content, embedding, embedding_model)
				 VALUES ($1, $2, $3, $4::vector, $5)`,
				id, chunk, fmt.Sprintf("scale chunk %d-%d", document, chunk),
				formatVector(randomUnitVector(source)), embedding.Model,
			)
		}
		if err := pool.SendBatch(ctx, batch).Close(); err != nil {
			t.Fatalf("insert scale chunks: %v", err)
		}
	}

	t.Logf("seeded %d documents x %d chunks = %d chunks in %s",
		documents, chunksPer, documents*chunksPer, time.Since(started).Round(time.Millisecond))

	return documentID
}

func TestPostgresSearchLatencyAtScale(t *testing.T) {
	raw := os.Getenv("RECALL_SCALE_DOCUMENTS")
	if raw == "" {
		t.Skip("set RECALL_SCALE_DOCUMENTS to run the scale check")
	}
	documents, err := strconv.Atoi(raw)
	if err != nil || documents < 1 {
		t.Fatalf("RECALL_SCALE_DOCUMENTS = %q, want a positive integer", raw)
	}

	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()

	const chunksPer = 20
	seedScaleCorpus(t, pool, documents, chunksPer)

	total := countRows(t, pool, `SELECT count(*) FROM document_chunks WHERE embedding IS NOT NULL`)
	t.Logf("corpus now holds %d embedded chunks", total)

	source := rand.New(rand.NewSource(2))
	const queries = 50
	durations := make([]time.Duration, 0, queries)
	for range queries {
		query := randomUnitVector(source)
		started := time.Now()
		if _, err := store.searchChunks(ctx, query, embedding.Model, 5, testOwnerID); err != nil {
			t.Fatalf("searchChunks: %v", err)
		}
		durations = append(durations, time.Since(started))
	}

	sort.Slice(durations, func(a, b int) bool { return durations[a] < durations[b] })
	t.Logf("search latency over %d queries against %d chunks: p50=%s p95=%s p99=%s max=%s",
		queries, total,
		durations[len(durations)*50/100].Round(time.Microsecond),
		durations[len(durations)*95/100].Round(time.Microsecond),
		durations[len(durations)*99/100].Round(time.Microsecond),
		durations[len(durations)-1].Round(time.Microsecond),
	)
}
