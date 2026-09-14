# Commit and deploy this release

Run section 1 on your **Mac**. After it succeeds, run section 2 in the **Lightsail server’s SSH terminal**. These commands are for the prepared release from `c6892ca` on `main`; do not run the server commands against the temporary preview.

## 1. Mac: create four commits and push

The changes are split into exact staging patches in `docs/commits/`. They stage only the relevant changes, including partial changes in shared files, without replacing your working files. The patches are local helpers in the ignored `docs/` directory; they are not deployed or published.

| Commit | Message | Contents |
|---|---|---|
| 1 | `fix: protect worker claims and run integration tests in CI` | Worker claim ownership, retries, Redis fallback, grading instruction and CI. |
| 2 | `feat: polish the study interface and add folders` | Responsive light/dark interface, folder management and scoped reviews. |
| 3 | `feat: retain and display original PDFs` | Private original-file storage, PDF reader, source-page links and PDF tests. |
| 4 | `docs: prepare Recall for release` | README, library screenshot and these upgrade instructions. |

Run this entire block in your Mac terminal. It stops if a command fails. It expects an empty staging area and the original starting commit; if either check fails, stop rather than reset or discard anything. Do not use `git add .` between these commands—it would mix the remaining features into the current commit.

```sh
(
  set -e
  cd ~/StudyBud/recall

  test "$(git branch --show-current)" = "main"
  test "$(git rev-parse HEAD)" = "c6892ca2add5f3dd2aeb8a5447f7338cff7087eb"
  git diff --cached --quiet

  # Commit 1: Worker claim ownership, retries, Redis fallback, grading instruction and CI.
  git apply --cached --check --whitespace=nowarn docs/commits/01-fix-worker-recovery-and-ci.patch
  git apply --cached --whitespace=nowarn docs/commits/01-fix-worker-recovery-and-ci.patch
  git commit -m "fix: protect worker claims and run integration tests in CI"

  # Commit 2: Responsive light/dark interface, folder management and scoped reviews.
  git apply --cached --check --whitespace=nowarn docs/commits/02-feat-polished-library-and-folders.patch
  git apply --cached --whitespace=nowarn docs/commits/02-feat-polished-library-and-folders.patch
  git commit -m "feat: polish the study interface and add folders"

  # Commit 3: Private original-file storage, PDF reader, source-page links and PDF tests.
  git apply --cached --check --whitespace=nowarn docs/commits/03-feat-original-pdf-viewing.patch
  git apply --cached --whitespace=nowarn docs/commits/03-feat-original-pdf-viewing.patch
  git commit -m "feat: retain and display original PDFs"

  # Commit 4: README, library screenshot and these upgrade instructions.
  git apply --cached --check --whitespace=nowarn docs/commits/04-docs-prepare-release.patch
  git apply --cached --whitespace=nowarn docs/commits/04-docs-prepare-release.patch
  git commit -m "docs: prepare Recall for release"

  git log -4 --oneline
  git status --short
  git push origin main
)
```

The tracked working tree should be clean afterward. Each code snapshot was tested separately before creating these patches. If the block stops partway through, the preceding commits remain valid; continue from the failed step after resolving its error rather than rerunning the whole block.

Wait for the pushed GitHub Actions run to pass before updating the live server. CI is configured to run Postgres and Redis integration tests, not just unit tests.

### Files in each commit

A shared file appearing in more than one list is intentional: the patch selects that feature’s version of the file.

#### Commit 1: `fix: protect worker claims and run integration tests in CI`

```text
.github/workflows/ci.yml
cmd/api/main.go
cmd/worker/main.go
compose.yaml
internal/api/cards.go
internal/api/cards_integration_test.go
internal/api/handler.go
internal/api/jobs.go
internal/api/jobs_integration_test.go
internal/api/pool_test.go
internal/api/postgres_store_integration_test.go
internal/api/recovery_integration_test.go
internal/api/release_integration_test.go
internal/api/reviews_integration_test.go
internal/api/worker.go
internal/api/worker_test.go
internal/generation/grade.go
internal/queue/redis.go
migrations/006_add_job_kind.sql
```

#### Commit 2: `feat: polish the study interface and add folders`

```text
internal/api/documents.go
internal/api/handler.go
internal/api/handler_test.go
internal/api/library.go
internal/api/postgres_store.go
internal/api/release_integration_test.go
internal/api/reviews.go
internal/api/reviews_test.go
internal/web/static/app.js
internal/web/static/ask.html
internal/web/static/ask.js
internal/web/static/favicon.svg
internal/web/static/index.html
internal/web/static/library.html
internal/web/static/library.js
internal/web/static/login.html
internal/web/static/login.js
internal/web/static/review.js
internal/web/static/style.css
migrations/011_add_folders.sql
```

#### Commit 3: `feat: retain and display original PDFs`

```text
internal/api/documents.go
internal/api/handler.go
internal/api/handler_test.go
internal/api/library.go
internal/api/postgres_store.go
internal/api/release_integration_test.go
internal/api/upload.go
internal/web/static/app.js
internal/web/static/library.js
internal/web/static/viewer.html
internal/web/static/viewer.js
migrations/012_store_original_pdfs.sql
internal/web/static/vendor/pdfjs/  (204 pinned upstream files, including licenses)
```

#### Commit 4: `docs: prepare Recall for release`

```text
.github/assets/library.png
README.md
deploy/UPGRADE.md
```

## 2. Lightsail: back up, upgrade and restart

Open the instance’s SSH terminal in the Lightsail console, or connect using your usual SSH command. The configured repository is `~/recall` on the server.

This assumes migrations 001–010 and the existing `recall-backup.service` are installed. The backup service already loads its credentials from its protected environment file. Do not paste credentials into this document.

Run this block **on the server**, after the four commits are pushed and CI passes:

```sh
(
  set -e
  cd ~/recall

  # Preserve the current revision for a code rollback.
  git rev-parse HEAD > /tmp/recall-before-upgrade.txt
  git pull --ff-only origin main

  # Finish a verified backup before touching the running application.
  sudo systemctl start recall-backup.service
  sudo journalctl -u recall-backup.service -n 15 --no-pager -o cat

  # Build first so the old application keeps running during the build.
  docker compose -f compose.yaml -f compose.prod.yaml build api worker

  # Stop writers, then apply the additive migrations to the existing database.
  docker compose -f compose.yaml -f compose.prod.yaml stop api worker
  docker compose -f compose.yaml -f compose.prod.yaml exec -T postgres psql -U recall -d recall -v ON_ERROR_STOP=1 --single-transaction < migrations/011_add_folders.sql
  docker compose -f compose.yaml -f compose.prod.yaml exec -T postgres psql -U recall -d recall -v ON_ERROR_STOP=1 --single-transaction < migrations/012_store_original_pdfs.sql

  docker compose -f compose.yaml -f compose.prod.yaml up -d api worker
  docker compose -f compose.yaml -f compose.prod.yaml ps
  docker compose -f compose.yaml -f compose.prod.yaml logs --tail=40 api worker
)
```

If a migration fails, the block stops and the API/worker remain stopped. Resolve the error before continuing to the remaining migration and restart commands. Do not delete the database volume or use `docker compose down -v`.

### Check the live app

Open the hosted Recall site and verify:

- Sign in with your existing account; confirm your notes and review progress are present.
- Create a folder, move a document into it, and start a folder review.
- Upload a small PDF, wait for questions, answer one, and open its source page in the original PDF.
- Sign out and test the shared demo account.

Then back up the upgraded data and run the existing restore drill:

```sh
(
  set -e
  cd ~/recall
  sudo systemctl start recall-backup.service
  sudo bash -c 'set -e; set -a; . /etc/recall/backup.env; set +a; cd /home/ubuntu/recall; ./scripts/restore-drill.sh'
)
```

For local Compose, omit `-f compose.yaml -f compose.prod.yaml`. New installations apply all migrations on their first database start.

## Existing accounts and documents

Accounts, cards, schedules and review history are preserved. Existing documents begin unfiled. Older uploads contain extracted text but no original PDF bytes; re-uploading retains the original as a new document with new cards. It does not transfer the old document’s review history. Keep the older document if you want to retain its progress.

The temporary preview database is separate from production and is not part of this deployment.

## Rollback

The migrations are additive. If the updated application fails, stop it and redeploy the previous code/images, using the revision saved in `/tmp/recall-before-upgrade.txt`. Leave the new tables and nullable folder column in place. Do not drop data as part of a code rollback. The older app can keep reviewing existing cards but will not retain PDFs for new uploads.

## Verification already completed locally

- Each commit’s code snapshot passed `go test ./...` independently.
- The complete release passed race-enabled tests with disposable Postgres/pgvector and Redis, plus a clean CI-style database check.
- Both Compose images built; existing-schema upgrade preserved a document, card, schedule and review.
- Restore checks matched table counts and original-PDF checksums.
- Real generation and a small grading smoke check passed; this is not a comprehensive AI accuracy evaluation.
- Browser checks covered folders, reviews, original PDF navigation/zoom and responsive light/dark layouts.
- A document completed processing with Redis stopped.

These are local results. Production verification is complete only after the server upgrade and live checks above.
