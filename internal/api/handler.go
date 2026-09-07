package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"uuid"
)

const maxRequestBodyBytes = 1 << 20 // 1 MiB, including the JSON wrapper.

var errDocumentNotFound = errors.New("document not found")

type documentStore interface {
	createDocument(ctx context.Context, content string) (string, error)
	getDocument(ctx context.Context, id string) (document, error)
}

type handler struct {
	store documentStore
}

type submitDocumentRequest struct {
	Content string `json:"content"`
}

type submitDocumentResponse struct {
	ID string `json:"id"`
}

type document struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func NewHandler(store documentStore) http.Handler {
	h := &handler{store: store}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("POST /documents", h.submitDocument)
	mux.HandleFunc("GET /documents/{id}", h.getDocument)
	return mux
}

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) submitDocument(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var request submitDocumentRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body must not exceed 1 MiB"})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body must contain valid JSON with a content field"})
		return
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body must contain exactly one JSON object"})
		return
	}

	if strings.TrimSpace(request.Content) == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "content must not be empty"})
		return
	}

	id, err := h.store.createDocument(r.Context(), request.Content)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not store document"})
		return
	}

	writeJSON(w, http.StatusCreated, submitDocumentResponse{ID: id})
}

func (h *handler) getDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "document id must be a valid UUID"})
		return
	}

	result, err := h.store.getDocument(r.Context(), id)
	if errors.Is(err, errDocumentNotFound) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "document not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not retrieve document"})
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
