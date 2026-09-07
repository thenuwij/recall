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
