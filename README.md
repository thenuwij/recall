# Recall

Recall will become a retrieval-augmented generation service written in Go. It accepts plain-text documents over HTTP, divides them into overlapping chunks, generates OpenAI embeddings, and stores the source content and vectors in PostgreSQL.

## Run Recall

Requirements: Go 1.27 or later, PostgreSQL with pgvector, and an OpenAI API key.

```sh
cp .env.example .env
# Add your OpenAI API key to .env.
set -a
source .env
set +a
go run ./cmd/api
```

Keep `OPENAI_API_KEY`, `DATABASE_URL`, and `PORT` in the ignored local `.env` file. Never commit the API key.

The server listens on `http://localhost:8080`. Set `PORT` to use another port.

```sh
curl -i http://localhost:8080/healthz

curl -i http://localhost:8080/documents \
  -H 'Content-Type: application/json' \
  -d '{"content":"Go handlers turn HTTP requests into responses."}'

curl -i http://localhost:8080/documents/<document-id>

curl -i http://localhost:8080/documents \
  -H 'Content-Type: application/json' \
  -d '{"content":""}'

curl -i http://localhost:8080/documents \
  -H 'Content-Type: application/json' \
  -d '{"content":'
```

Run the automated checks with:

```sh
go test ./...
```

The request body limit is 1 MiB, including the JSON wrapper.

## Request flow

1. `net/http` routes a request to the matching handler.
2. `POST /documents` decodes and validates JSON, then divides the content into overlapping word-based chunks.
3. The embedding client sends the chunks to OpenAI before a database transaction begins.
4. The PostgreSQL store saves the original document, chunks, vectors, model IDs, and embedding timestamps in one transaction.
5. `GET /documents/{id}` validates the UUID before retrieving the matching row.
6. `POST /search` validates the query and limit, embeds the query with the same model, and asks PostgreSQL for the nearest chunk vectors.
7. The handler translates results into JSON and meaningful HTTP status codes.

The handler depends on a small storage interface. The running application uses PostgreSQL, while handler tests use an in-memory implementation.

## Milestone 1B PostgreSQL persistence

Start PostgreSQL with:

```sh
docker compose up -d postgres
docker compose ps
```

The development database is available at `localhost:5434`. The database name, username, and password are all `recall`. Port 5434 avoids other local PostgreSQL projects using the default port.

Apply the first migration with:

```sh
docker compose exec -T postgres psql -U recall -d recall < migrations/001_create_documents.sql
```

The migration enables pgvector for later milestones and creates a `documents` table containing a UUID, non-blank document text, and a creation timestamp.

The Go API uses pgx to create and retrieve documents in PostgreSQL. A stored document can be retrieved after restarting the API because its data belongs to PostgreSQL rather than the Go process.

## Milestone 2 document chunking

Apply the chunk-storage migration after migration 001:

```sh
docker compose exec -T postgres psql -U recall -d recall < migrations/002_create_document_chunks.sql
```

New documents are split into chunks containing at most 200 whitespace-separated words. Consecutive chunks overlap by 40 words so context near a boundary is available to both chunks. Chunk whitespace is normalized, while the original document content remains unchanged.

The API stores the document and its ordered, zero-indexed chunks in a single PostgreSQL transaction. If any insert fails, PostgreSQL rolls back the document and all preceding chunk inserts together. Documents created before migration 002 are not backfilled automatically and may have no chunks.

## Milestone 3 embeddings

Apply the embedding-storage migration after migration 002:

```sh
docker compose exec -T postgres psql -U recall -d recall < migrations/003_add_chunk_embeddings.sql
```

Recall uses OpenAI `text-embedding-3-small` and explicitly requests 1,536-dimensional float vectors. The client batches large input sets, restores results using provider indexes, validates every vector dimension, and applies a 30-second timeout.

Embedding generation happens before PostgreSQL begins its transaction, avoiding an open database transaction while waiting on a network service. If OpenAI fails or returns malformed data, Recall responds with `502 Bad Gateway` and stores nothing. The new columns are nullable so chunks created before this milestone remain valid and can be backfilled later.

## Milestone 4 semantic retrieval

`POST /search` embeds a query with the same model used for stored chunks and returns the nearest chunks as evidence. No migration is required; retrieval reads the columns added by migration 003.

```sh
curl -i http://localhost:8080/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"How is data protected if a server suddenly fails?","limit":5}'
```

```json
{
  "results": [
    {
      "chunk_id": "20a050d5-b4d0-426b-84a0-37093a0e1e9d",
      "document_id": "9e4a6ca8-5462-451c-a0d4-9858a67c5bc4",
      "chunk_index": 0,
      "content": "Information written to disk endures beyond ...",
      "similarity": 0.3833
    }
  ]
}
```

`query` must not be blank. `limit` is optional, defaults to 5, and must be between 1 and 20; an explicit `0` is rejected rather than treated as absent. Unknown fields, trailing JSON, and bodies above 1 MiB are rejected before any provider call, so an invalid request never costs an embedding.

`similarity` is `1 - cosine_distance`, so higher means more relevant and `1.0` means identical direction. Results are ordered nearest first, with `(document_id, chunk_index)` breaking ties deterministically. Chunks with no embedding, or embedded by a different model, are excluded: vectors from different models are not comparable and would produce meaningless scores rather than an error.

An empty result set is a successful search that found no evidence. It returns `200` with `{"results":[]}`, never `null`.

A provider failure returns `502 Bad Gateway`, matching document ingestion. A database failure returns `500`.

Retrieval performs an exact scan and there is no vector index yet. That is deliberate: an approximate index trades recall for speed, and the trade needs a measured baseline before it is worth making.

### PostgreSQL integration tests

Tests that require a database are skipped unless a connection string is provided:

```sh
RECALL_TEST_DATABASE_URL='postgres://recall:recall@localhost:5434/recall?sslmode=disable' \
  go test ./internal/api/ -run TestPostgres -v -count=1
```

They insert a fixture with hand-built vectors, assert the ranking and exclusions, and delete the fixture afterwards. Without the variable they report `SKIP`, which is not the same as passing.

## Documentation map

- `README.md`: what Recall does and how to run it. This is the only documentation kept in the repository.

Design decisions, milestone evidence, and learning notes are maintained outside version control.
