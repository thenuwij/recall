# Recall - Study Companion

[![CI](https://github.com/thenuwij/recall/actions/workflows/ci.yml/badge.svg)](https://github.com/thenuwij/recall/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?logo=postgresql&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-7-DC382D?logo=redis&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-Compose-2496ED?logo=docker&logoColor=white)

Recall is a personal study application I built to explore turning documents into source-linked practice questions and scheduling reviews from free-text answers. It combines document ingestion, vector retrieval, LLM generation and grading, and an SM-2-based spaced repetition scheduler.

## Project demonstration

Explore the [project demonstration](https://recall.thenujawijesuriya.com), including the **Explore the demo** option on the sign-in page. Demo reviews are graded without saving progress.

## Implemented features

- Email/password registration and login, bcrypt password hashing, database-backed cookie sessions, and account-scoped documents and reviews.
- PDF, UTF-8 text and Markdown uploads, processing status, and folder organisation.
- Background embedding and practice-question generation with stored source passages and PDF page references.
- Free-text answer grading on a 0–5 scale, feedback and suggested answers, followed by spaced repetition scheduling. Reviews can cover all due cards or a document or folder.
- Semantic search and answers generated from retrieved passages, with citations and an insufficient-evidence response path.
- Original PDF storage and a PDF.js viewer with page navigation, zoom and source-page links.

## Stack and architecture

| Component | Implementation |
| --- | --- |
| Backend | Go 1.27, standard-library HTTP server, pgx/v5 |
| Database | PostgreSQL 16 with pgvector; 1,536-dimensional embeddings |
| Notifications | Redis 7 Streams with go-redis/v9 |
| AI services | OpenAI `text-embedding-3-small` for embeddings; `gpt-4o-mini` for questions, grading and answers |
| Frontend | Embedded HTML, CSS and vanilla JavaScript; bundled PDF.js |
| PDF extraction | Poppler's `pdftotext` executable |
| Packaging | Multi-stage Docker builds, Docker Compose; optional Caddy HTTPS proxy |

```text
Browser → API → PostgreSQL (documents, chunks, jobs, sessions, cards, reviews)
           │       ▲
           │       └── Worker → OpenAI (embeddings, questions)
           ├── Redis Streams → Worker (wake-up notifications)
           └── OpenAI (query embeddings, answers, grading)
```

The API extracts PDF text, splits documents into overlapping chunks, and stores documents and ingestion jobs. Workers claim jobs in PostgreSQL using `FOR UPDATE SKIP LOCKED`, renew leases, and retry failed work with backoff. Redis provides notifications; workers also poll PostgreSQL when notifications are unavailable.

Frontend assets are embedded in the API binary, with no separate frontend build. Original PDF bytes are stored in PostgreSQL.

## Repository structure

```text
cmd/                   API and worker entry points
internal/              handlers, storage, AI clients, extraction and scheduling
internal/web/          embedded frontend and PDF.js assets
migrations/            ordered SQL schema migrations
scripts/               database backup and restore verification
.github/workflows/     CI checks
Dockerfile             API and worker image targets
compose.yaml           local services
compose.prod.yaml      deployment overrides and Caddy
```

## Local setup

### Prerequisites

- Git, Docker Engine or Docker Desktop, and Docker Compose v2 or later.
- An OpenAI API key with billing enabled and access to the models listed above. Both services require it at startup.
- For host Go commands: Go 1.27 as declared in `go.mod`, and Poppler with `pdftotext` on `PATH`. Install Poppler using `brew install poppler` on macOS or `sudo apt-get install poppler-utils` on Debian/Ubuntu. The API Docker image installs it automatically.

### Environment variables

Copy `.env.example` to `.env` and set `OPENAI_API_KEY` privately. `.env` is ignored by Git. The example connection strings are for the bundled local services.

| Variable | Purpose and default |
| --- | --- |
| `OPENAI_API_KEY` | Required for API and worker; no default. |
| `DATABASE_URL` | Required. Example: `postgres://recall:recall@localhost:5434/recall?sslmode=disable`. |
| `REDIS_URL` | Required. Example: `redis://localhost:6381/0`.  |
| `PORT` | API port; default `8080`. |
| `WORKER_CONCURRENCY` | Worker pool size; default `2`. |
| `WORKER_HEALTH_PORT` | Worker health server port; default `8081`. |
| `LOG_LEVEL` | Structured log level: `DEBUG`, `INFO`, `WARN` or `ERROR`; default `INFO`. |

Compose overrides database and Redis URLs with container-network addresses and sets the API container port to `8080`.

### Run everything in Docker

1. Clone and configure the environment:

   ```sh
   git clone https://github.com/thenuwij/recall.git
   cd recall
   cp .env.example .env
   # Edit .env and set OPENAI_API_KEY before starting services.
   ```

2. Build and start:

   ```sh
   docker compose up --build -d
   docker compose ps
   docker compose logs --tail=50 api worker
   ```

   On the first start with an empty PostgreSQL volume, the database image runs SQL files in `migrations/` in filename order through `/docker-entrypoint-initdb.d`. This creates the schema and pgvector extension. No separate seed command is needed.

   Existing volumes do not rerun migrations. Apply new SQL migrations manually in filename order when updating an existing database.

3. Open [localhost:8090](http://localhost:8090), register an account and upload a small text file or text-based PDF. The worker generates embeddings and questions in the background. Local accounts start without sample content.

4. Stop with `docker compose down`. Named volumes retain PostgreSQL and Redis data.

### Run Go services on the host

Start dependencies, install Go modules and load the environment into your shell:

```sh
docker compose up -d postgres redis
go mod download
set -a
. ./.env
set +a
go run ./cmd/api
```

In a second terminal in the repository:

```sh
set -a
. ./.env
set +a
go run ./cmd/worker
```

Open [localhost:8080](http://localhost:8080) with the example `PORT`. PostgreSQL is exposed on `5434` and Redis on `6381`. Stop any containerised API/worker first to avoid duplicate workers or conflicting health-server ports.

## Tests, lint and builds

Run from the repository root with Go 1.27:

```sh
# Service-dependent tests skip unless configured below.
go test -race -count=1 ./...

# CI formatting check: expect no filenames.
gofmt -l internal cmd

# Static analysis.
go vet ./...

# Compile both services without writing binaries into the repository.
go build ./cmd/api ./cmd/worker

# Validate Compose configuration and build both images.
docker compose config --quiet
docker compose build
```

For PostgreSQL and Redis integration tests, start local dependencies and create a separate test database once:

```sh
docker compose up -d postgres redis
# Wait for PostgreSQL to report healthy in docker compose ps.
docker compose exec -T postgres createdb -U recall recall_test
export RECALL_TEST_DATABASE_URL='postgres://recall:recall@localhost:5434/recall_test?sslmode=disable'
export RECALL_TEST_REDIS_URL='redis://localhost:6381/0'
go test -race -count=1 ./...
```

Skip `createdb` if `recall_test` exists. Integration tests skip without these URLs; PDF extraction tests skip without `pdftotext`. OpenAI client tests use HTTP fixtures. CI runs formatting, vet, race-enabled tests and a Compose build with PostgreSQL, Redis and Poppler configured.

## Contributing

Open an issue or submit a focused pull request describing the change. Add relevant tests, run the checks above, and include an ordered migration for schema changes. Keep credentials and `.env` out of commits.

## License

Licensed under the [MIT License](LICENSE). Bundled PDF.js and supporting assets retain their [upstream licenses](internal/web/static/vendor/pdfjs/LICENSE) and accompanying asset-specific license files.
