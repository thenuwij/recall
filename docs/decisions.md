# Design decisions

This file records important choices and their trade-offs. We only add a decision when understanding the reason will help us maintain or explain Recall.

## 1. Start with Go's standard HTTP library

**Decision:** Use `net/http` and `encoding/json` instead of a web framework.

**Why:** The standard library keeps the request lifecycle visible while learning: routing, decoding, validation, and response writing are not hidden behind framework abstractions.

**Trade-off:** We write some plumbing ourselves, but the project has fewer dependencies and the fundamentals are easier to see.

## 2. Use temporary memory before PostgreSQL

**Decision:** Milestone 1A stores documents in a mutex-protected Go map. Milestone 1B replaces it with PostgreSQL.

**Why:** This separated learning the HTTP request flow from learning database access.

**Trade-off:** The map loses all documents when the API restarts and cannot be shared by multiple API processes.

## 3. Limit request bodies to 1 MiB

**Decision:** Limit the complete JSON request body to 1 MiB.

**Why:** A caller should not be able to make the server read an unexpectedly large body without a limit.

**Trade-off:** The limit includes the JSON wrapper and may need adjustment after measuring realistic documents.

## 4. Put PostgreSQL behind a storage interface

**Decision:** HTTP handlers depend on a small document storage interface rather than directly calling pgx.

**Why:** The handler remains focused on HTTP concerns, PostgreSQL queries stay in the store, and tests can use an in-memory implementation.

**Trade-off:** Every new storage operation must be added to the interface and implemented by both the production and test stores.

## 5. Start with word-based overlapping chunks

**Decision:** Split new documents into chunks of at most 200 whitespace-separated words with a 40-word overlap. Store their order using a zero-based `chunk_index`.

**Why:** Word windows are deterministic and easy to inspect while building the first retrieval pipeline. Overlap preserves nearby context when relevant text crosses a chunk boundary.

**Trade-off:** Word counts do not match model token counts, and fixed windows do not respect paragraph or semantic boundaries. We can replace this strategy after measuring retrieval quality.

## 6. Store documents and chunks atomically

**Decision:** Insert the original document and all derived chunks in one PostgreSQL transaction.

**Why:** A document without all of its chunks would be incomplete retrieval data. A transaction makes the whole write succeed or fail as one unit.

**Trade-off:** The transaction stays open for every chunk insert. We may later batch inserts if larger documents make this too slow.

## 7. Do not backfill existing documents yet

**Decision:** Migration 002 creates chunk storage without generating chunks for documents that already exist.

**Why:** Schema migration and data backfill have different operational risks. Keeping them separate makes the initial migration simple and lets us design a restartable backfill when it is needed.

**Trade-off:** Pre-chunking documents cannot participate in retrieval until a later backfill processes them.
