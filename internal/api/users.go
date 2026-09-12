package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookieName = "recall_session"
	sessionLifetime   = 30 * 24 * time.Hour
	minPasswordLength = 8
)

var (
	errEmailTaken      = errors.New("email already registered")
	errUserNotFound    = errors.New("user not found")
	errSessionNotFound = errors.New("session not found")
)

type user struct {
	ID                  string `json:"id"`
	Email               string `json:"email"`
	MaxDocuments        *int   `json:"max_documents"`
	MaxPagesPerDocument *int   `json:"max_pages_per_document"`
}

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type contextKey string

const userContextKey contextKey = "user"

func userFromContext(ctx context.Context) (user, bool) {
	value, ok := ctx.Value(userContextKey).(user)
	return value, ok
}

func newSessionToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func normaliseEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (h *handler) register(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var request credentialsRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body must contain valid JSON with email and password fields"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body must contain exactly one JSON object"})
		return
	}

	email := normaliseEmail(request.Email)
	if email == "" || !strings.Contains(email, "@") {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "email must be a valid address"})
		return
	}
	if len(request.Password) < minPasswordLength {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "password must be at least 8 characters"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(request.Password), bcrypt.DefaultCost)
	if err != nil {
		h.logger.Printf("auth: hash password: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not create the account"})
		return
	}

	created, err := h.store.createUser(r.Context(), email, string(hash))
	if errors.Is(err, errEmailTaken) {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "email already registered"})
		return
	}
	if err != nil {
		h.logger.Printf("auth: create user: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not create the account"})
		return
	}

	h.startSession(w, r, created)
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var request credentialsRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body must contain valid JSON with email and password fields"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body must contain exactly one JSON object"})
		return
	}

	found, hash, err := h.store.userByEmail(r.Context(), normaliseEmail(request.Email))
	if errors.Is(err, errUserNotFound) {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "email or password is incorrect"})
		return
	}
	if err != nil {
		h.logger.Printf("auth: look up user: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not sign in"})
		return
	}

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(request.Password)) != nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "email or password is incorrect"})
		return
	}

	h.startSession(w, r, found)
}

func (h *handler) startSession(w http.ResponseWriter, r *http.Request, account user) {
	token, err := newSessionToken()
	if err != nil {
		h.logger.Printf("auth: generate session token: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not start a session"})
		return
	}

	expiresAt := time.Now().Add(sessionLifetime)
	if err := h.store.createSession(r.Context(), token, account.ID, expiresAt); err != nil {
		h.logger.Printf("auth: create session: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not start a session"})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})

	writeJSON(w, http.StatusOK, account)
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		if err := h.store.deleteSession(r.Context(), cookie.Value); err != nil {
			h.logger.Printf("auth: delete session: %v", err)
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) currentUser(w http.ResponseWriter, r *http.Request) {
	account, ok := userFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "sign in to continue"})
		return
	}

	writeJSON(w, http.StatusOK, account)
}

func (h *handler) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "sign in to continue"})
			return
		}

		account, err := h.store.userBySession(r.Context(), cookie.Value)
		if errors.Is(err, errSessionNotFound) {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "sign in to continue"})
			return
		}
		if err != nil {
			h.logger.Printf("auth: look up session: %v", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not verify the session"})
			return
		}

		next(w, r.WithContext(context.WithValue(r.Context(), userContextKey, account)))
	}
}
