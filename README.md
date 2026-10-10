# Recall

[![CI](https://github.com/thenuwij/recall/actions/workflows/ci.yml/badge.svg)](https://github.com/thenuwij/recall/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?logo=postgresql&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-7-DC382D?logo=redis&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-Compose-2496ED?logo=docker&logoColor=white)

Recall turns your own notes into practice questions and asks them back just before you would forget them. It is for students and anyone learning from lecture slides, papers or documentation. Upload your notes once, then spend a few minutes a day answering questions in your own words.

## Try the demo

Open [Recall](https://recall.thenujawijesuriya.com) and choose **Explore the demo** on the sign-in screen. There's no sign-up, and practice questions are ready to answer straight away.

The demo also lets you upload one PDF of up to 10 pages. Answers are graded but progress is not saved, so every visitor sees the same questions. To use Recall with your own notes, create an account on the same screen.

## Key Features

### Notes

Upload a PDF, text or Markdown file and Recall writes questions from it, usually within a minute.

- Each note shows when its questions are ready, with a button to review them
- Folders sit on the same page as your notes, and a note moves into one from its menu
- Upload while a folder is open and the note goes straight into it

### Review

Recall asks one question at a time and you answer in your own words.

- Each answer gets a score out of 5, an explanation and a suggested answer
- The passage the question came from is shown, highlighted in your notes
- Questions you struggle with come back sooner, and ones you know well wait longer
- Review everything due, or just one note or folder

### Original PDFs

Open any uploaded PDF in the app, with page navigation and zoom. After an answer, a link takes you to the page the question came from.

### Ask

Ask a question and Recall answers from your notes, citing where each part came from. If your notes don't cover it, Recall says so instead of guessing.

## How It Works

Two Go services share one Postgres database. The API serves the app and grades answers. A worker reads documents, stores their embeddings and writes questions.

```text
Browser → API ── documents, jobs, reviews ──→ Postgres
           │                                     ▲
           │                                     │ claim jobs, save results
           └──→ Redis Streams ── wake-up ──→ Worker ── embeddings, questions ──→ OpenAI
```

- Postgres owns every job, and Redis only wakes the worker, so work continues while Redis is down
- Workers claim jobs with `FOR UPDATE SKIP LOCKED` and hold a renewable lease, so a crashed worker's job is picked up again and finished once
- Answers are graded against the stored source passage, and model replies with invalid scores are rejected
- The hosted app runs on AWS Lightsail behind Caddy, with nightly backups to S3 and a tested restore

## Built With

Go, PostgreSQL with pgvector, Redis, Docker Compose, OpenAI and PDF.js. [Try it live](https://recall.thenujawijesuriya.com).

## Run Locally

You need Docker and an OpenAI API key.

```sh
git clone https://github.com/thenuwij/recall.git
cd recall
cp .env.example .env
```

Set `OPENAI_API_KEY` in `.env`, then run `docker compose up --build` and open [localhost:8090](http://localhost:8090). Run the tests with `go test -race ./...`.

## Project Structure

```text
cmd/          api and worker entry points
internal/     the app: API, chunking, embeddings, PDF extraction, question generation, queue, scheduling, web pages
migrations/   database schema
scripts/      backup and restore drill
```

## License

[MIT](LICENSE). Bundled PDF.js and its supporting assets retain their [upstream licenses](internal/web/static/vendor/pdfjs/LICENSE).
