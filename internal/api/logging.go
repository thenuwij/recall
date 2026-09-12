package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/http"
	"time"
)

const requestIDContextKey contextKey = "request-id"

func newRequestID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "unknown"
	}

	return base64.RawURLEncoding.EncodeToString(raw)
}

func requestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey).(string)
	return value
}

func (h *handler) log(ctx context.Context) *slog.Logger {
	logger := h.logger
	if id := requestIDFromContext(ctx); id != "" {
		logger = logger.With("request_id", id)
	}
	if account, ok := userFromContext(ctx); ok {
		logger = logger.With("user_id", account.ID)
	}

	return logger
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (h *handler) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		id := newRequestID()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r.WithContext(context.WithValue(r.Context(), requestIDContextKey, id)))

		h.logger.Info("request",
			"request_id", id,
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}
