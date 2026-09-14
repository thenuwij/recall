# Recall

[![CI](https://github.com/thenuwij/recall/actions/workflows/ci.yml/badge.svg)](https://github.com/thenuwij/recall/actions/workflows/ci.yml)

Recall turns your notes into practice questions and schedules reviews using spaced repetition. Answer in your own words, get feedback against the source passage, and return to the original PDF when you need more context.

![Recall library with folders and study documents](.github/assets/library.png)

## Try Recall

Open [Recall](https://recall.thenujawijesuriya.com), create an account, or choose **Explore the demo** on the sign-in screen.

The demo account is `demo@recall.app` / `recalldemo123`. It includes practice questions and allows one PDF upload of up to 10 pages. Answers are graded but review progress is not saved. Folder editing requires your own account so the shared demo stays consistent.

## Features

- **Study your own material:** upload PDF, text or Markdown notes to generate Q&A cards.
- **Organise by topic:** create folders, move documents, and review a folder or a single document. Due reviews take priority over new cards.
- **Understand your answers:** see a grade, explanation, suggested answer and highlighted source excerpt after each response.
- **Read the original:** open uploaded PDFs with page navigation and zoom. Source feedback links to the relevant PDF page.
- **Build a review habit:** SM-2 schedules the next review from your answer grade.
- **Ask your notes:** the existing Ask screen searches your library and returns answers with source citations.

The interface adapts to phone, tablet and desktop, and follows your system's light or dark appearance.

## Run locally

You need Docker and an OpenAI API key. Go, Postgres, Redis and PDF text extraction run in containers.

```sh
git clone https://github.com/thenuwij/recall.git
cd recall
cp .env.example .env
```

Set `OPENAI_API_KEY` in `.env`, then start the app:

```sh
docker compose up --build
```

Open [localhost:8090](http://localhost:8090), create an account and upload a document. The Library shows processing status and the number of generated questions. Open Review when they are ready.

`docker compose down` stops the services. Adding `-v` also deletes the database volume and its uploaded files.

Migrations run automatically on an empty database volume. Existing installations must apply new migrations before running the updated API; see [upgrade instructions](deploy/UPGRADE.md).

## Architecture

Two Go binaries share Postgres: the API serves the embedded frontend, and a small worker pool handles embeddings and question generation. PDF.js is bundled locally to render original PDFs; there is no frontend build server or runtime CDN dependency.

```text
Upload → API → Postgres: document, chunks, original PDF and queued job
          │
          └── Redis Streams: worker wake-up
                    │
                  Worker → embeddings → questions → review schedule
                    │
                  Postgres
```

### Jobs and recovery

Postgres owns job state. A job is inserted in the same transaction as its document, and Redis is notified after the commit. Workers also poll Postgres every two seconds and can start and process jobs while Redis is unavailable.

Redis Streams is a deliberate messaging exercise in this project: it separates worker notifications from durable job storage. At this deployment size, Postgres polling alone would also be sufficient. No comparative Redis performance benefit is claimed.

Workers claim jobs with `FOR UPDATE SKIP LOCKED`. Claims have a two-minute lease, renewed every 40 seconds, and an incrementing attempt number. Completion, renewal and failure updates check the claim's attempt number so an old worker cannot modify a replacement worker's job. Result writes and completion commit together.

Transient provider errors retry with backoff, up to three attempts. Abandoned final attempts become failed jobs rather than retrying indefinitely. Failed jobs retain their last error. Model calls can repeat after a crash; database effects are protected against stale completion.

### Source material and model calls

Each card points to its source chunk. Grading uses that chunk directly, rather than searching again. Model responses are validated: invalid scores and unknown source references are rejected.

Embeddings, question generation and grading use direct HTTP calls in [`internal/embedding`](internal/embedding) and [`internal/generation`](internal/generation). The workflow is small enough to keep timeouts, validation and retry decisions explicit.

Original PDFs are stored in a separate Postgres table and served through authenticated, owner-scoped endpoints. This keeps file retention and backups in one storage system. Deleting a document also removes its file, questions and review history; deleting a folder only unfiles its documents.

## Testing and performance

With Go and `pdftotext` installed:

```sh
go test -race ./...
```

To include database and Redis tests, start the Compose services and run:

```sh
RECALL_TEST_DATABASE_URL='postgres://recall:recall@localhost:5434/recall?sslmode=disable' \
RECALL_TEST_REDIS_URL='redis://localhost:6381/0' \
go test -race -count=1 ./...
```

Postgres tests create and remove their own schemas, including migrations. The test database account needs permission to create schemas and the vector extension. Redis tests use separate stream names. CI provisions both services and runs the suite with the race detector.

Coverage includes expired claims, stale worker updates, duplicate completion, retry exhaustion, folder review isolation, private PDF access, byte-range responses and concurrent review submissions.

These are historical measurements from earlier individual runs, not guarantees for the current release:

| Measurement | Result | Context |
|---|---|---|
| Search, 40,000 chunks | p50: 416 ms; p95: 502 ms | Exact scan, no vector index |
| Search, 5,000 chunks | p50: 36 ms; p95: 40 ms | Exact scan, no vector index |
| Worker shutdown | About 30 s → 1.28 s | After introducing the bounded worker pool |
| Restore drill | 8 s | Small, 184 KB database |

The opt-in search test is `TestPostgresSearchLatencyAtScale`, configured through `RECALL_SCALE_DOCUMENTS`.

## Deployment

The hosted app runs on a 2 GB AWS Lightsail instance with Postgres, Redis and Caddy:

```sh
docker compose -f compose.yaml -f compose.prod.yaml up -d --build
```

Caddy handles TLS. Only SSH and HTTP/HTTPS ports are published in production; the API, worker, Postgres and Redis use the internal network. The API and worker retry database connections during startup.

A systemd timer runs [nightly backups](scripts/backup.sh) to a private S3 bucket with seven-day retention. The database dump includes original PDFs. The [restore drill](scripts/restore-drill.sh) restores a backup into a temporary database and compares table row counts. Lightsail snapshots provide another recovery option.

## Limitations

- Q&A is the only card format. Folder reviews follow due dates rather than document-size weighting.
- Source excerpts are highlighted in the feedback panel; original PDF pages are shown without phrase overlays.
- PDFs uploaded before file retention was added have extracted text only. Re-upload to retain the original; existing review progress is left intact.
- Scanned or image-only PDFs need OCR elsewhere before upload. Extraction and page text order can vary with complex layouts.
- AI grading can make mistakes. A small manual smoke check is not a comprehensive quality evaluation.
- Vector search is an exact scan, and the single server is a single point of failure. Deployment and database upgrades are manual.

## License

[MIT](LICENSE). Bundled PDF.js and its supporting assets retain their [upstream licenses](internal/web/static/vendor/pdfjs/LICENSE).
