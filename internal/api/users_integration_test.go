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
