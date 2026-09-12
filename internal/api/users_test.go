package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func registerTestUser(t *testing.T, handler http.Handler, email, password string) *http.Cookie {
	t.Helper()

	body := `{"email":"` + email + `","password":"` + password + `"}`
	request := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("register status = %d, want %d (%s)", response.Code, http.StatusOK, response.Body.String())
	}

	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			return cookie
		}
	}

	t.Fatalf("register did not set a %s cookie", sessionCookieName)
	return nil
}

func TestRegisterCreatesAccountAndSession(t *testing.T) {
	store := newMemoryStore()
	handler := newTestHandler(store)

	cookie := registerTestUser(t, handler, "learner@example.com", "correct horse")

	if cookie.Value == "" {
		t.Fatal("session cookie is empty")
	}
	if !cookie.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}

	store.mu.RLock()
	hash := store.hashes["learner@example.com"]
	store.mu.RUnlock()
	if hash == "" {
		t.Fatal("password hash was not stored")
	}
	if strings.Contains(hash, "correct horse") {
		t.Fatal("password was stored in plain text")
	}
}

func TestRegisterNormalisesEmail(t *testing.T) {
	store := newMemoryStore()

	registerTestUser(t, newTestHandler(store), "  Learner@Example.COM  ", "correct horse")

	store.mu.RLock()
	_, exists := store.users["learner@example.com"]
	store.mu.RUnlock()
	if !exists {
		t.Fatal("email was not normalised to lower case and trimmed")
	}
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "blank email", body: `{"email":"  ","password":"correct horse"}`},
		{name: "email without at sign", body: `{"email":"learner","password":"correct horse"}`},
		{name: "short password", body: `{"email":"learner@example.com","password":"short"}`},
		{name: "unknown field", body: `{"email":"learner@example.com","password":"correct horse","role":"admin"}`},
		{name: "two objects", body: `{"email":"a@b.com","password":"correct horse"}{}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(testCase.body))
			response := httptest.NewRecorder()

			newTestHandler(newMemoryStore()).ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestRegisterRejectsDuplicateEmail(t *testing.T) {
	handler := newTestHandler(newMemoryStore())
	registerTestUser(t, handler, "learner@example.com", "correct horse")

	body := `{"email":"learner@example.com","password":"another password"}`
	request := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
	}
}

func TestLoginAcceptsCorrectPassword(t *testing.T) {
	handler := newTestHandler(newMemoryStore())
	registerTestUser(t, handler, "learner@example.com", "correct horse")

	body := `{"email":"learner@example.com","password":"correct horse"}`
	request := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (%s)", response.Code, http.StatusOK, response.Body.String())
	}

	var account user
	if err := json.NewDecoder(response.Body).Decode(&account); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if account.Email != "learner@example.com" {
		t.Fatalf("email = %q, want %q", account.Email, "learner@example.com")
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	handler := newTestHandler(newMemoryStore())
	registerTestUser(t, handler, "learner@example.com", "correct horse")

	cases := []struct {
		name string
		body string
	}{
		{name: "wrong password", body: `{"email":"learner@example.com","password":"wrong password"}`},
		{name: "unknown email", body: `{"email":"stranger@example.com","password":"correct horse"}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(testCase.body))
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
			}
			if strings.Contains(response.Body.String(), "password is incorrect\"}\n") == false {
				t.Fatalf("body = %q, want a generic message", response.Body.String())
			}
		})
	}
}

func TestCurrentUserRequiresSession(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	response := httptest.NewRecorder()

	newTestHandler(newMemoryStore()).ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestCurrentUserReturnsTheSignedInAccount(t *testing.T) {
	handler := newTestHandler(newMemoryStore())
	cookie := registerTestUser(t, handler, "learner@example.com", "correct horse")

	request := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}

	var account user
	if err := json.NewDecoder(response.Body).Decode(&account); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if account.Email != "learner@example.com" {
		t.Fatalf("email = %q, want %q", account.Email, "learner@example.com")
	}
}

func TestCurrentUserRejectsUnknownSession(t *testing.T) {
	handler := newTestHandler(newMemoryStore())

	request := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "not-a-real-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestLogoutClearsTheSession(t *testing.T) {
	store := newMemoryStore()
	handler := newTestHandler(store)
	cookie := registerTestUser(t, handler, "learner@example.com", "correct horse")

	request := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}

	store.mu.RLock()
	_, stillThere := store.sessions[cookie.Value]
	store.mu.RUnlock()
	if stillThere {
		t.Fatal("session was not deleted")
	}

	followUp := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	followUp.AddCookie(cookie)
	followUpResponse := httptest.NewRecorder()
	handler.ServeHTTP(followUpResponse, followUp)

	if followUpResponse.Code != http.StatusUnauthorized {
		t.Fatalf("status after logout = %d, want %d", followUpResponse.Code, http.StatusUnauthorized)
	}
}

func TestProtectedRoutesRequireASession(t *testing.T) {
	cases := []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/documents"},
		{method: http.MethodPost, path: "/documents/upload"},
		{method: http.MethodGet, path: "/documents"},
		{method: http.MethodGet, path: "/documents/9e2b1f8c-0000-4000-8000-000000000000"},
		{method: http.MethodDelete, path: "/documents/9e2b1f8c-0000-4000-8000-000000000000"},
		{method: http.MethodGet, path: "/chunks/9e2b1f8c-0000-4000-8000-000000000000/context"},
		{method: http.MethodPost, path: "/search"},
		{method: http.MethodPost, path: "/answer"},
		{method: http.MethodGet, path: "/reviews/due"},
		{method: http.MethodPost, path: "/reviews/9e2b1f8c-0000-4000-8000-000000000000"},
	}

	handler := newTestHandler(newMemoryStore())
	for _, testCase := range cases {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader("{}"))
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestHealthAndWebRemainPublic(t *testing.T) {
	handler := newTestHandler(newMemoryStore())

	for _, path := range []string{"/healthz", "/"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		if response.Code == http.StatusUnauthorized {
			t.Fatalf("%s returned %d, want it to stay public", path, http.StatusUnauthorized)
		}
	}
}
