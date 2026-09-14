# Recall

[![CI](https://github.com/thenuwij/recall/actions/workflows/ci.yml/badge.svg)](https://github.com/thenuwij/recall/actions/workflows/ci.yml)

Upload your lecture notes and Recall quizzes you on them. It writes questions from the PDF, asks them back on a spaced-repetition schedule, and grades what you type against the exact passage the question came from, highlighted on its page.

Live at https://recall.thenujawijesuriya.com. To try it without signing up, sign in with `demo@recall.app` and `recalldemo123`. The demo account already has a document with questions. Your answers are graded but not saved, so everyone gets the same questions, and you can upload one PDF of up to 10 pages.

Written in Go, with Postgres and pgvector for storage and search, Redis for waking the worker, and OpenAI for embeddings, question writing and grading.

## Run it

You need Docker and an OpenAI API key. Go, Postgres, Redis and `pdftotext` all live in the containers.

```sh
git clone https://github.com/thenuwij/recall.git
cd recall
cp .env.example .env               # then put your key in OPENAI_API_KEY
docker compose up --build
```

Open http://localhost:8090, make an account and upload a PDF. The Library shows it go `ready`, then its card count, and Review has your first questions. A 24-chunk lecture took about 13 seconds end to end.

`docker compose down` stops it, and `down -v` wipes the database too.

The tests run with `go test -race ./...`. The ones that need a real database skip themselves unless you point them at the Compose services:

```sh
RECALL_TEST_DATABASE_URL='postgres://recall:recall@localhost:5434/recall?sslmode=disable' \
RECALL_TEST_REDIS_URL='redis://localhost:6381/0' \
go test -race ./...
```

## How it works

```
upload
   |
 API       document + chunks + a queued job, one Postgres transaction
   |       then a nudge on a Redis stream
   |
 worker    claims the job from Postgres, embeds every chunk
   |       same transaction queues a card job
   |
 worker    writes questions per chunk, drops near-duplicates, schedules them
   |
 review    your answer is graded 0-5 against that chunk, SM-2 picks the next date
```

There are two binaries, an API and a worker, sharing one database. The web interface is plain HTML and JavaScript embedded in the API binary, so there is one thing to build and ship, and the UI can never be a different version from the API behind it.

Every card points at the chunk it was written from, with its page and offsets. Grading doesn't search again; it compares your answer with that chunk. So when you get something wrong, what you're shown is what the document actually said, not something that happened to sound similar.

## Job queue

Postgres and Redis can't commit together. Publish a job inside the transaction and you can announce work that then rolls back; publish before it and a failed commit loses the job. So the upload writes the job row in the same transaction as the document, and only tells Redis after the commit. That's a transactional outbox.

Workers never take work from Redis. The message is just a wake-up, and the worker then claims whatever is next from Postgres:

```sql
UPDATE ingestion_jobs SET state = 'processing', claimed_until = now() + lease, attempts = attempts + 1
WHERE id = (SELECT id FROM ingestion_jobs WHERE <claimable> ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
```

`SKIP LOCKED` means any number of workers can run this at once without two of them getting the same row. The lease means a worker that dies mid-job doesn't hold it forever: two minutes later it's claimable again. A running job renews its lease every 40 seconds. The worker also polls Postgres every 2 seconds, so a lost Redis message costs two seconds, a duplicated one costs nothing, and restarting Redis loses no work at all.

Attempts are counted when a job is claimed, not when it fails, because a worker that gets killed never gets to report a failure. Count failures and a job that crashes its worker retries forever. Rate limits, timeouts and 5xx retry with backoff, three attempts in total; a 400 or a malformed response fails straight away, since retrying would do the identical thing again. Failed jobs keep their `last_error` in the table, which is the dead-letter queue.

## Failure testing

At-least-once delivery means any job might run twice, so everything a job writes has to be safe to repeat. I thought it was. Then I wrote tests that do the unpleasant things on purpose: kill a worker holding a job, complete a job twice, claim with Redis switched off.

The double completion failed. `completeCardJob` inserted the cards and marked the job done without checking whether it was still in progress. So if a slow worker's lease ran out and a second worker reclaimed and finished the job, the first one would eventually finish too, and insert every card again.

The fix is one condition: the completion only updates the job if it's still `processing`. If it isn't, no rows change, the function returns an error, and the transaction takes the cards back out with it ([`a32c89d`](https://github.com/thenuwij/recall/commit/a32c89d)). Take the condition out and the test fails again.

The tests are in [`recovery_integration_test.go`](internal/api/recovery_integration_test.go).

## The worker pool

The worker started as one loop: claim, process, repeat. It's now a fixed pool of goroutines (`WORKER_CONCURRENCY`, default 2), each renewing its own lease. On shutdown it stops claiming, cancels what's running and exits. The job it dropped isn't marked failed; its lease runs out and another worker picks it up.

The race detector runs on every CI build, and it caught a real data race the first time the pool ran.

## Numbers

Only things I actually measured.

| | |
|---|---|
| Worker shutdown | ~30 s → **1.28 s**, after the bounded pool and a 2 s poll |
| Search, 40,000 chunks, no vector index | p50 **416 ms**, p95 **502 ms** |
| Search, 5,000 chunks, no vector index | p50 36 ms, p95 40 ms |
| Images | api **77 MB**, worker **39 MB** |
| Restore drill | 8 s on a 184 KB database |

Search is an exact scan, so it grows with the corpus. The latency test is `TestPostgresSearchLatencyAtScale`, opt-in with `RECALL_SCALE_DOCUMENTS`.

## Where it runs

One AWS Lightsail instance, 2 GB, running the same Compose file with [`compose.prod.yaml`](compose.prod.yaml) on top:

```sh
docker compose -f compose.yaml -f compose.prod.yaml up -d --build
```

The overlay adds Caddy, which gets the TLS certificate by itself, and unpublishes every other port. Only 22, 80 and 443 are open, so Postgres, Redis and the API aren't reachable from outside. After a reboot Docker starts every container at once and ignores Compose's health-check ordering, so the API and worker retry the database for up to a minute instead of crashing.

Backups run nightly from a systemd timer ([`deploy/`](deploy/)). [`backup.sh`](scripts/backup.sh) takes a `pg_dump`, checks it with `pg_restore --list` before trusting it, and uploads it to a private S3 bucket that keeps seven days. The server's IAM key can upload and download there but can't delete, so a compromised box can't take its backups down with it. [`restore-drill.sh`](scripts/restore-drill.sh) restores the newest one into a throwaway database and compares every table's row count. Lightsail snapshots sit underneath as a second copy.

## Model calls

Recall makes three kinds of model call: embed, write cards, grade. Each is a plain HTTP request in [`internal/embedding`](internal/embedding) or [`internal/generation`](internal/generation), so the timeouts, retry decisions and response validation are all code you can read. Model output is checked, not trusted: a card citing a chunk it wasn't given is thrown away, and a grade that's missing, fractional or out of range is an error rather than a default score. Document text only ever goes in the user message, never the system prompt.

## Scaling

`SKIP LOCKED` doesn't care how many workers there are or which machine they're on, so more work means more workers. The API keeps its sessions in Postgres, so it can run as several copies behind Caddy.

## License

[MIT](LICENSE)
