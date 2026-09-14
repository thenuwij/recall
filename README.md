# Recall

[![CI](https://github.com/thenuwij/recall/actions/workflows/ci.yml/badge.svg)](https://github.com/thenuwij/recall/actions/workflows/ci.yml)

Recall turns lecture PDFs into practice questions and schedules reviews using spaced repetition. It grades your answers against the source material and highlights the relevant passage in the PDF, so you can check the explanation in context.

Built with Go, Postgres, pgvector, Redis and OpenAI. The web interface uses plain HTML and JavaScript.

## Try Recall

Open [Recall](https://recall.thenujawijesuriya.com) and create an account, or use the demo account:

- **Email:** `demo@recall.app`
- **Password:** `recalldemo123`

The demo includes a document with practice questions and lets you upload one PDF of up to 10 pages. Answers are graded but aren't saved, so each visitor can try the same questions.

## Features

- **Questions from your notes:** Upload a lecture PDF to generate practice questions from its contents.
- **Source-based feedback:** Answers are graded against the passage used to create the question, with the source highlighted in the PDF.
- **Spaced repetition:** Review dates follow the SM-2 algorithm, using answer grades from 0 to 5.
- **Background processing:** A worker processes uploads and generates questions while you use the app.

## Run locally

You need Docker and an OpenAI API key. Go, Postgres, Redis and `pdftotext` run in containers.

```sh
git clone https://github.com/thenuwij/recall.git
cd recall
cp .env.example .env
```

Set `OPENAI_API_KEY` in `.env`, then start the services:

```sh
docker compose up --build
```

Open [localhost:8090](http://localhost:8090), create an account and upload a PDF. Track processing in the Library, then open Review once questions are available.

To stop the services, run `docker compose down`. To also delete the database volume, run `docker compose down -v`.

## Architecture

Recall has two binaries: an API and a worker. Both use Postgres. The HTML and JavaScript are embedded in the API binary and shipped with it.

```text
Upload PDF
    |
    v
API        Save the document, chunks and ingestion job in one transaction
    |      Notify the worker through Redis after the commit
    v
Worker     Embed the chunks and queue a card-generation job
    |
    v
Worker     Generate questions, remove near-duplicates and schedule reviews
    |
    v
Review     Grade the answer against its source chunk and set the next review date
```

### Source references and model calls

Each card stores a reference to its source chunk, including the page and text offsets. Grading uses that chunk directly rather than performing another search.

OpenAI handles embeddings, question generation and grading through HTTP requests in [`internal/embedding`](internal/embedding) and [`internal/generation`](internal/generation). These packages define timeouts, retries and response validation. Cards that cite an unknown chunk are rejected, as are grades that are missing, fractional or outside the allowed range. Document text is passed in the user message, never the system prompt.

### Job processing

Postgres is the source of truth for jobs; Redis provides wake-up notifications. The API writes each job in the same transaction as its document and notifies Redis after the commit. Workers also poll Postgres every two seconds, so processing can continue if a notification is lost or Redis is unavailable.

Workers claim jobs with `FOR UPDATE SKIP LOCKED`, allowing multiple workers to claim separate rows concurrently. Each claim has a two-minute lease, renewed every 40 seconds while the job runs. If a worker stops, the job becomes available again when its lease expires.

Attempts are counted when a job is claimed, including attempts interrupted by a worker crash. Rate limits, timeouts and server errors retry with backoff, up to three attempts in total. A 400 response or malformed model response fails immediately. Failed jobs remain in the table with their `last_error` for inspection.

The worker uses a fixed pool of goroutines, configured with `WORKER_CONCURRENCY` (default: 2). On shutdown, it stops claiming jobs and cancels active work. Interrupted jobs remain available for recovery after their leases expire.

### Scaling

Additional workers can share the same Postgres job queue across processes or machines. API sessions are also stored in Postgres, allowing multiple API instances behind Caddy.

Vector search currently uses an exact scan without a vector index. Search latency therefore increases with the number of stored chunks; measurements are listed below.

## Testing and performance

With Go installed, run:

```sh
go test -race ./...
```

Integration tests that require external services are skipped unless connection URLs are provided. With the Compose services running:

```sh
RECALL_TEST_DATABASE_URL='postgres://recall:recall@localhost:5434/recall?sslmode=disable' \
RECALL_TEST_REDIS_URL='redis://localhost:6381/0' \
go test -race ./...
```

The [recovery integration tests](internal/api/recovery_integration_test.go) cover worker termination, duplicate job completion and job claims while Redis is unavailable. Duplicate-completion testing exposed a bug that could insert the same cards twice. The [fix](https://github.com/thenuwij/recall/commit/a32c89d) checks the job's state during completion and rolls back card inserts if the job is no longer processing.

CI runs the tests with the Go race detector, which caught a data race during development of the worker pool.

### Recorded measurements

These results describe individual test runs, rather than performance guarantees.

| Measurement | Result |
|---|---|
| End-to-end processing, 24-chunk lecture | About 13 s |
| Worker shutdown | Reduced from about 30 s to 1.28 s after introducing the bounded pool and a 2 s poll interval |
| Search, 40,000 chunks, no vector index | p50: 416 ms; p95: 502 ms |
| Search, 5,000 chunks, no vector index | p50: 36 ms; p95: 40 ms |
| Docker image sizes | API: 77 MB; worker: 39 MB |
| Restore drill, 184 KB database | 8 s |

The search latency test, `TestPostgresSearchLatencyAtScale`, is opt-in through `RECALL_SCALE_DOCUMENTS`.

## Deployment

The hosted app runs on one AWS Lightsail instance with 2 GB of memory. Production uses the base Compose file with [`compose.prod.yaml`](compose.prod.yaml):

```sh
docker compose -f compose.yaml -f compose.prod.yaml up -d --build
```

The production overlay adds Caddy for automatic TLS and removes published ports from the internal services. Only ports 22, 80 and 443 are open externally; Postgres, Redis and the API are accessed through the internal network.

After a host reboot, Docker restarts containers without Compose's health-check ordering. The API and worker retry their database connections for up to a minute to allow Postgres to start.

### Backups and recovery

A systemd timer runs nightly backups; configuration is in [`deploy/`](deploy/). The [backup script](scripts/backup.sh) creates a `pg_dump`, checks that `pg_restore --list` can read the archive and uploads it to a private S3 bucket with seven-day retention. The server's IAM credentials allow uploads and downloads but do not grant deletion permissions.

The [restore drill](scripts/restore-drill.sh) restores the latest backup into a temporary database and compares row counts for every table. Lightsail snapshots provide an additional recovery option.

## License

[MIT](LICENSE)
