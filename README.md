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
6. The handler translates results into JSON and meaningful HTTP status codes.

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

## Documentation map

- `README.md`: what Recall does and how to run it.
- `docs/learning-notes.md`: concepts learned and milestone evidence.
- `docs/decisions.md`: important design choices and trade-offs.
