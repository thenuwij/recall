# Learning notes

## Milestone 1A

- An HTTP handler translates transport input (HTTP and JSON) into typed Go values, applies validation, calls storage, and translates the result back into an HTTP response.
- `http.MaxBytesReader` bounds the whole request body before JSON decoding, limiting memory and work spent on unexpectedly large input.
- JSON decoding proves that input has the expected syntax and shape; validation separately proves that `content` is meaningful rather than empty or whitespace.
- The in-memory map is process-local and volatile: stopping the process discards the map. Milestone 1B replaces it with PostgreSQL.

### Evidence

- `go test ./...` passed tests for health, successful submission, temporary storage, malformed JSON, empty content, and the 1 MiB request limit.
- A valid `POST /documents` returned `201 Created` with a document ID.
- Blank and malformed content intentionally returned `400 Bad Request`.
- An oversized request intentionally returned `413 Request Entity Too Large`.

## Milestone 1B: database foundation

- PostgreSQL stores data outside the Go process, so documents can survive an API restart.
- A migration is versioned SQL that changes the database structure in a repeatable way.
- The `documents` table gives every document a UUID, requires non-blank text, and records when it was created.
- The database repeats the non-blank constraint as a final safety boundary even though the API also validates input.
- A storage interface separates HTTP behaviour from PostgreSQL details and lets handler tests use an in-memory store.
- A parameterized query uses `$1` to pass an ID separately from the SQL statement.
- A malformed UUID is a `400 Bad Request`; a valid UUID with no matching row is a `404 Not Found`.
- Go's `time.Time` becomes an RFC 3339 timestamp when encoded as JSON.

### Evidence

- `go test ./...` passed the API handler tests after adding document retrieval.
- `POST /documents` returned `201 Created` with a PostgreSQL UUID.
- A direct SQL query found the same UUID, content, and creation timestamp in the `documents` table.
- After the Go API restarted, `GET /documents/{id}` returned the same stored document.
- Live requests returned `400 Bad Request` for a malformed UUID and `404 Not Found` for a valid missing UUID.

## Milestone 2: document chunking

- Chunking converts a large document into smaller retrieval units while retaining the unchanged source document.
- `strings.Fields` treats consecutive whitespace as separators and gives chunks a consistent single-space representation.
- With a 200-word maximum and 40-word overlap, the splitter advances by 160 words for each new chunk.
- Validating that overlap is smaller than the maximum guarantees a positive step and prevents an infinite loop.
- `chunk_index` records deterministic ordering independently of chunk UUIDs.
- A database transaction groups the document insert and every chunk insert into one atomic operation. Returning before commit causes the deferred rollback to discard partial data.
- `ON DELETE CASCADE` ensures deleting a document also deletes its dependent chunks.
- A schema migration does not automatically transform old rows. Existing documents need a separately designed backfill before they can participate in retrieval.

### Evidence

- Focused chunking tests passed for invalid configuration, blank input, short and exact-size input, overlapping windows, whitespace normalization, and Unicode text.
- Focused API tests passed and confirmed that a submitted document produces the expected chunk passed to storage.
- `go test ./...` passed across the project.
- PostgreSQL migration 002 was present in the development database.
- A 201-word API submission preserved the original document exactly and created two chunks at indexes 0 and 1.
- The stored chunks contained 200 and 41 words. Chunk 1 began at word 161, and its first 40 words exactly matched chunk 0's final 40 words.
- Both chunks referenced the submitted document through `document_id`.

## Milestone 3: embeddings

- An embedding represents text as a fixed-length vector. Related text should occupy nearby positions, enabling semantic rather than exact-keyword search.
- Recall uses OpenAI `text-embedding-3-small` with 1,536 dimensions. The application requests the dimension explicitly and validates every response before storage.
- The embedding client sits behind a small handler interface. Production uses OpenAI, while tests use a deterministic fake or local `httptest.Server`.
- Provider response indexes are used to restore input order instead of assuming response array order.
- Large chunk collections are divided into bounded provider requests. An error in any batch prevents database storage.
- The API key is read from `OPENAI_API_KEY`; secrets stay in an ignored `.env` file and must not be logged or committed.
- Embedding calls happen before the PostgreSQL transaction. This avoids holding a transaction open during network latency.
- A provider failure maps to `502 Bad Gateway` because Recall acted as a gateway to an upstream dependency. Storage failures remain `500 Internal Server Error`.
- pgvector accepts a bracketed vector representation such as `[1,-2.5,0]`. Recall builds that value separately and passes it as a parameter cast to `vector`, rather than concatenating it into SQL.
- Nullable embedding fields make older chunks identifiable for a later backfill. Retrieval must ignore null vectors.

### Evidence

- Focused embedding client tests passed for authentication and request shape, output ordering, empty input, provider HTTP failure, result count, index validation, and vector dimensions.
- Focused API tests passed for vector attachment, model metadata, provider failure, and the rule that a failed embedding stores no document.
- `go test ./...` passed across the project.
- Migration 003 applied successfully and PostgreSQL reported nullable `vector`, `text`, and `timestamptz` columns.
- One authorized live request created document `4b412905-3d69-490b-a208-f71db7086112` with a linked chunk containing a 1,536-dimensional `text-embedding-3-small` vector and embedding timestamp.
