# Recall

Recall will become a retrieval-augmented generation service written in Go. It accepts plain-text documents over HTTP and stores them in PostgreSQL.

## Run Recall

Requirements: Go 1.27 or later.

```sh
DATABASE_URL='postgres://recall:recall@localhost:5434/recall?sslmode=disable' go run ./cmd/api
```

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
2. `POST /documents` decodes and validates JSON before inserting content through the PostgreSQL store.
3. `GET /documents/{id}` validates the UUID before retrieving the matching row.
4. The handler translates results into JSON and meaningful HTTP status codes.

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

## Documentation map

- `README.md`: what Recall does and how to run it.
- `docs/learning-notes.md`: concepts learned and milestone evidence.
- `docs/decisions.md`: important design choices and trade-offs.
