package api

import (
	"context"
	"errors"
	"testing"
	"time"
)

func createTestUser(t *testing.T, store *PostgresStore, email string) user {
	t.Helper()

	created, err := store.createUser(context.Background(), email, "hash-"+email)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, created.ID)
	})

	return created
}

func TestPostgresCreateUserRejectsDuplicateEmail(t *testing.T) {
	store := NewPostgresStore(testPool(t))
	created := createTestUser(t, store, "duplicate@example.com")

	_, err := store.createUser(context.Background(), created.Email, "another-hash")

	if !errors.Is(err, errEmailTaken) {
		t.Fatalf("error = %v, want %v", err, errEmailTaken)
	}
}

func TestPostgresUserByEmailReturnsTheStoredHash(t *testing.T) {
	store := NewPostgresStore(testPool(t))
	created := createTestUser(t, store, "lookup@example.com")

	found, hash, err := store.userByEmail(context.Background(), created.Email)
	if err != nil {
		t.Fatalf("look up user: %v", err)
	}

	if found.ID != created.ID {
		t.Fatalf("id = %q, want %q", found.ID, created.ID)
	}
	if hash != "hash-"+created.Email {
		t.Fatalf("hash = %q, want %q", hash, "hash-"+created.Email)
	}
	if found.MaxDocuments != nil {
		t.Fatalf("max documents = %v, want nil for an unlimited account", *found.MaxDocuments)
	}
}

func TestPostgresUserByEmailReportsMissingUser(t *testing.T) {
	store := NewPostgresStore(testPool(t))

	_, _, err := store.userByEmail(context.Background(), "absent@example.com")

	if !errors.Is(err, errUserNotFound) {
		t.Fatalf("error = %v, want %v", err, errUserNotFound)
	}
}

func TestPostgresSessionRoundTrip(t *testing.T) {
	store := NewPostgresStore(testPool(t))
	created := createTestUser(t, store, "session@example.com")

	token := "integration-token-live"
	if err := store.createSession(context.Background(), token, created.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create session: %v", err)
	}

	found, err := store.userBySession(context.Background(), token)
	if err != nil {
		t.Fatalf("look up session: %v", err)
	}
	if found.ID != created.ID {
		t.Fatalf("id = %q, want %q", found.ID, created.ID)
	}

	if err := store.deleteSession(context.Background(), token); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := store.userBySession(context.Background(), token); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("error after delete = %v, want %v", err, errSessionNotFound)
	}
}

func TestPostgresExpiredSessionIsRejected(t *testing.T) {
	store := NewPostgresStore(testPool(t))
	created := createTestUser(t, store, "expired@example.com")

	token := "integration-token-expired"
	if err := store.createSession(context.Background(), token, created.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := store.userBySession(context.Background(), token)

	if !errors.Is(err, errSessionNotFound) {
		t.Fatalf("error = %v, want %v", err, errSessionNotFound)
	}
}

func TestPostgresDeletingUserRemovesSessions(t *testing.T) {
	store := NewPostgresStore(testPool(t))
	created := createTestUser(t, store, "cascade@example.com")

	token := "integration-token-cascade"
	if err := store.createSession(context.Background(), token, created.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, created.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	if _, err := store.userBySession(context.Background(), token); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("error = %v, want %v", err, errSessionNotFound)
	}
}

func TestPostgresDocumentsAreScopedToTheirOwner(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	stranger := createTestUser(t, store, "stranger@example.com")

	id := createTestDocument(t, store, pool, newDocument{Title: "Owned", SourceType: sourceText, Content: "one two three four five six"})
	ctx := context.Background()

	if _, err := store.getDocument(ctx, id, stranger.ID); !errors.Is(err, errDocumentNotFound) {
		t.Fatalf("getDocument as a stranger = %v, want %v", err, errDocumentNotFound)
	}

	documents, err := store.listDocuments(ctx, stranger.ID)
	if err != nil {
		t.Fatalf("listDocuments: %v", err)
	}
	for _, summary := range documents {
		if summary.ID == id {
			t.Fatal("listDocuments returned another user's document")
		}
	}

	if err := store.deleteDocument(ctx, id, stranger.ID); !errors.Is(err, errDocumentNotFound) {
		t.Fatalf("deleteDocument as a stranger = %v, want %v", err, errDocumentNotFound)
	}

	if _, err := store.getDocument(ctx, id, testOwnerID); err != nil {
		t.Fatalf("the owner can no longer read their own document: %v", err)
	}
}

func TestPostgresSearchIsScopedToTheirOwner(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	stranger := createTestUser(t, store, "search-stranger@example.com")

	createTestDocument(t, store, pool, newDocument{Title: "Owned", SourceType: sourceText, Content: "one two three four five six"})
	ctx := context.Background()

	results, err := store.searchChunks(ctx, testVector(1, 0), "text-embedding-3-small", 20, stranger.ID)
	if err != nil {
		t.Fatalf("searchChunks: %v", err)
	}

	if len(results) != 0 {
		t.Fatalf("search returned %d chunks for a user who owns nothing, want 0", len(results))
	}
}
