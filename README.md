# Recall

Recall is a retrieval-augmented generation service written in Go. It accepts plain-text documents over HTTP, divides them into overlapping chunks, generates OpenAI embeddings, and stores the source content and vectors in PostgreSQL. It answers questions by retrieving the most relevant passages and generating an answer that cites them, or by refusing when the evidence is too weak.

## Run Recall

Requirements: Go 1.27 or later, PostgreSQL with pgvector, Redis, poppler's `pdftotext` (`brew install poppler`), and an OpenAI API key.

```sh
cp .env.example .env
# Add your OpenAI API key to .env.
set -a
source .env
set +a
go run ./cmd/api
```

Recall runs as two processes. Start the ingestion worker alongside the API:

```sh
go run ./cmd/worker
```

Documents are accepted by the API but embedded by the worker, so without the worker running a submitted document stays `queued` and never becomes searchable.

Keep `OPENAI_API_KEY`, `DATABASE_URL`, `REDIS_URL`, and `PORT` in the ignored local `.env` file. Never commit the API key.

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
3. The PostgreSQL store saves the document, its chunks without vectors, and a queued ingestion job in one transaction, then returns `202 Accepted`.
4. The ingestion worker claims the job, sends its chunks to OpenAI, and writes the vectors and the completed job state in one transaction.
5. `GET /documents/{id}` validates the UUID before retrieving the matching row.
6. `POST /search` validates the query and limit, embeds the query with the same model, and asks PostgreSQL for the nearest chunk vectors.
7. `POST /answer` retrieves the same evidence, refuses if it is too weak, and otherwise asks a language model for an answer that cites the passages it used.
8. The handler translates results into JSON and meaningful HTTP status codes.

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

## Milestone 4 part A semantic retrieval

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

## Milestone 4 part B cited answers

`POST /answer` retrieves evidence, then asks a language model to write an answer grounded in it. No migration is required.

```sh
curl -i http://localhost:8080/answer \
  -H 'Content-Type: application/json' \
  -d '{"query":"How is data protected if a server fails?","limit":5}'
```

```json
{
  "answer": "Data is protected from partial updates through the use of transactions ... [1]",
  "citations": [
    {
      "marker": 1,
      "chunk_id": "20a050d5-b4d0-426b-84a0-37093a0e1e9d",
      "document_id": "9e4a6ca8-5462-451c-a0d4-9858a67c5bc4",
      "chunk_index": 0,
      "content": "Information written to disk endures beyond ...",
      "similarity": 0.3851
    }
  ],
  "refused": false
}
```

`limit` defaults to 5 and must be between 1 and 10. Validation matches `POST /search`, so an invalid request never reaches a provider.

### Refusal

Recall refuses rather than guessing, and a refusal is a success:

```json
{ "answer": "", "citations": [], "refused": true, "reason": "no sufficiently relevant evidence was found" }
```

There are two refusal paths, distinguishable by their reason:

- **Before generation.** If retrieval returns nothing, or the best similarity is below `0.25`, the request is refused with no prompt built and no model call made. Nothing is spent.
- **After generation.** If the model returns an empty answer or one containing no citations, the answer is refused rather than served as ungrounded prose.

The `0.25` floor is a starting point measured against a small corpus, not a calibrated value. It should be re-measured against a labelled evaluation set.

### Citations

Every claim must cite the passage it came from using `[1]`, `[2]` markers. Each marker is checked against the passages actually supplied; a marker outside that range means the model cited something that does not exist, and returns `502 Bad Gateway`. Duplicate markers collapse to one citation and citations are returned in ascending order.

A citation proves the source was available to the model. It does not prove the passage supports the claim — those are different problems, and only the first is mechanically checkable.

### Grounding and untrusted documents

Documents are user-submitted, so retrieved text is placed only in the user message, never in the system prompt, and the model is instructed to treat any instructions inside passages as quoted text to report on rather than obey. This limits prompt-injection exposure but does not eliminate it, which is why citation validation is enforced separately.

Answer generation uses OpenAI `gpt-4o-mini` with `temperature: 0` and a bounded completion length, reusing `OPENAI_API_KEY`. A provider failure returns `502`; a database failure returns `500`.

## Documentation map

- `README.md`: what Recall does and how to run it. This is the only documentation kept in the repository.

Milestone headings here match the numbered build pages kept outside the repository. Milestone 4 covers retrieval and cited answers as parts A and B of one page, as it does there.

Design decisions, milestone evidence, and learning notes are maintained outside version control.

## Milestone 5 background ingestion

Embedding moved off the request path. Apply the ingestion job migration after migration 003:

```sh
docker compose exec -T postgres psql -U recall -d recall < migrations/004_create_ingestion_jobs.sql
```

Start Redis alongside PostgreSQL:

```sh
docker compose up -d postgres redis
```

Redis is available at `localhost:6381`. `POST /documents` now returns `202 Accepted` rather than `201 Created`:

```json
{ "id": "8a68b11b-4ed7-4d02-8b02-987daf57bf87", "status": "queued" }
```

`202` means the document is durable, not that it is searchable. `GET /documents/{id}` reports progress as `queued`, `processing`, `ready`, or `failed` with a reason. Documents created before migration 004 have no job row and report `unknown` rather than a guessed status.

### How a document is ingested

The API writes the document, its chunks with null embeddings, and an `ingestion_jobs` row at `queued` in a single transaction. That job row is a transactional outbox entry: PostgreSQL and Redis cannot commit together, so nothing is written to Redis inside the transaction. After the commit the job id is published to a Redis stream, and a failed publish is logged rather than returned, because the queued row is still in PostgreSQL.

The worker claims a job with `SELECT ... FOR UPDATE SKIP LOCKED` under a two-minute lease, embeds only the chunks that still have a null embedding, and writes the vectors and the terminal job state in one transaction. Retrieval already excludes chunks with no embedding, so a document is invisible to search until its vectors exist rather than partially searchable.

### Delivery and idempotency

Redis delivers a wake-up, not a work item: the job id is carried for logging, and the worker claims whatever job is next. PostgreSQL decides which worker gets which job, so a duplicated notification is harmless and a lost one costs latency rather than work.

The worker also claims directly from PostgreSQL every 30 seconds. Redis removes that latency; the poll is the backstop that catches a lost notification or a lease that expired because a worker stalled. Duplicate processing cannot duplicate data, because `UNIQUE (document_id, chunk_index)` already exists and chunking is deterministic.

A message is acknowledged only after the transaction that writes its vectors commits. Acknowledging earlier would discard Redis's record of a job whose result was never stored.

### Retries and failure

Failures are classified before any retry. A provider `429`, `408`, or `5xx`, a timeout, or a lost connection is retryable. A provider `400`, a malformed response, or a wrong vector dimension is permanent, because retrying repeats identical work to fail identically.

A retryable failure returns the job to `queued` with `claimed_until` set into the future, which is the backoff: roughly 5 seconds, then 30 seconds. Three attempts in total. A job that exhausts its attempts, or fails permanently, ends at `failed` with `attempts` and `last_error` recorded. Those rows are the dead letter and are inspectable with a query, so no separate dead-letter stream exists.

Attempts are counted when a job is claimed rather than when a failure is reported. A worker killed mid-job never reports anything, so counting reported failures would let a job that kills its worker retry forever.

Shutting a worker down is not a job failure. The job is left claimed, its lease expires, and another worker takes it without an attempt being spent.

### Backfilling documents that predate embedding

Chunks created before milestone 003 have null embeddings and no job. Giving them one makes them ordinary ingestion work:

```sh
docker compose exec -T postgres psql -U recall -d recall -c \
  "INSERT INTO ingestion_jobs (document_id) SELECT DISTINCT document_id FROM document_chunks WHERE embedding IS NULL ON CONFLICT (document_id, kind) DO NOTHING;"
```

### Redis integration tests

Tests that require Redis are skipped unless a connection string is provided:

```sh
RECALL_TEST_REDIS_URL='redis://localhost:6381/0' go test ./internal/queue/ -v
```

As with the PostgreSQL integration tests, a skipped test is not a passing test.

## Milestone 8 documents, PDFs and source locations

Chunks now record where they came from. Apply the source-location migration after migration 004:

```sh
docker compose exec -T postgres psql -U recall -d recall < migrations/005_add_source_locations.sql
```

Documents gain an optional `title` and a `source_type` of `text` or `pdf`. Each chunk records `start_offset` and `end_offset`, the byte range of its words in the original content, and PDF chunks record a `page_number`. A form feed in the content marks a page break, and no chunk crosses one. Rows created before migration 005 keep null offsets; re-ingest them rather than backfilling.

Upload a PDF, text, or Markdown file as multipart form data. The title is the filename without its extension:

```sh
curl -i http://localhost:8080/documents/upload -F 'file=@Lecture 3.pdf'
```

PDF text is extracted with `pdftotext`, which separates pages with form feeds, so every chunk of a PDF records its page. The API refuses to start if `pdftotext` is not installed. Files are limited to 25 MiB. A scanned PDF with no text layer, or a password-protected PDF, is rejected with `422` and a reason; a file that is not really a PDF, or any other file type, is rejected with `415`.

`POST /documents` also accepts an optional `title`:

```sh
curl -i http://localhost:8080/documents \
  -H 'Content-Type: application/json' \
  -d '{"title":"Week 1","content":"Go handlers turn HTTP requests into responses."}'
```

List documents, newest first, or delete one along with its chunks and jobs:

```sh
curl -i http://localhost:8080/documents
curl -i -X DELETE http://localhost:8080/documents/<document-id>
```

Citations from `POST /answer` and results from `POST /search` now carry the document `title` and, for PDFs, the `page`. To show a passage where it sits, fetch its context:

```sh
curl -i http://localhost:8080/chunks/<chunk-id>/context
```

The response splits the surrounding text into `before`, `passage`, and `after`, so a client can highlight the passage without offset arithmetic. Offsets are bytes in Go but UTF-16 units in JavaScript, and the two disagree at the first accented character. For a PDF the surrounding text is the passage's page; for a text document it is up to about 500 bytes either side, cut at word boundaries. Chunks stored before migration 005 have no location and return `409`.

## Milestone 11 card generation

Ingestion jobs gain a `kind`, so one document can have an embedding job and a card-generation job. Apply the migration after migration 005:

```sh
docker compose exec -T postgres psql -U recall -d recall < migrations/006_add_job_kind.sql
```

Existing jobs become `embed` jobs, and each document may have at most one job of each kind. When an embedding job completes, the same transaction queues a `generate_cards` job for the document, and the worker claims it on its next pass without a Redis notification. Documents report `status` from the embedding job and `cards_status` from the card job, which is `not_started` until embedding finishes.

Cards, their review schedules, and the review log come next. Apply after migration 006:

```sh
docker compose exec -T postgres psql -U recall -d recall < migrations/007_create_cards.sql
```

The worker needs `OPENAI_API_KEY` for both embedding and card generation. A card job sends the document's chunks to `gpt-4o-mini` in batches of five and asks for at most two questions per chunk, each with an expected answer and the number of the chunk it came from. Go discards any card that names a chunk it was not given, has a blank question or answer, or exceeds two for its chunk. A chunk with nothing worth testing may produce no cards.

Neighbouring chunks overlap, so the same fact can produce two nearly identical questions. Every question is embedded, and one within 0.92 cosine similarity of a question already kept is dropped. The cards, a schedule row for each (due immediately), and the job completion are written in one transaction, so a retried job never duplicates cards. Documents report `card_count` alongside `cards_status`.

Documents embedded before migration 006 have no card job. Queue one for each:

```sh
docker compose exec -T postgres psql -U recall -d recall -c \
  "INSERT INTO ingestion_jobs (document_id, kind) SELECT document_id, 'generate_cards' FROM ingestion_jobs WHERE kind = 'embed' AND state = 'completed' ON CONFLICT (document_id, kind) DO NOTHING;"
```
