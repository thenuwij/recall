package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/thenujawijesuriya/recall/internal/chunking"
	"github.com/thenujawijesuriya/recall/internal/generation"
	"github.com/thenujawijesuriya/recall/internal/web"
)

const (
	maxRequestBodyBytes      = 1 << 20 // 1 MiB, including the JSON wrapper.
	defaultChunkMaxWords     = 200
	defaultChunkOverlapWords = 40
)

var errDocumentNotFound = errors.New("document not found")

type documentStore interface {
	createDocument(ctx context.Context, doc newDocument, chunks []chunking.Chunk) (string, string, error)
	getDocument(ctx context.Context, id, userID string) (document, error)
	listDocuments(ctx context.Context, userID string) ([]documentSummary, error)
	deleteDocument(ctx context.Context, id, userID string) error
	chunkLocation(ctx context.Context, chunkID, userID string) (chunkLocation, error)
	searchChunks(ctx context.Context, queryEmbedding []float32, model string, limit int, userID string) ([]searchResult, error)
	dueCards(ctx context.Context, limit, newCardCap int, userID string) ([]dueCard, error)
	nextDueAt(ctx context.Context, userID string, newCardCap int) (*time.Time, error)
	reviewCard(ctx context.Context, cardID, userID string) (reviewCard, error)
	recordReview(ctx context.Context, review newReview) error
	countDocuments(ctx context.Context, userID string) (int, error)
	createUser(ctx context.Context, email, passwordHash string) (user, error)
	userByEmail(ctx context.Context, email string) (user, string, error)
	createSession(ctx context.Context, token, userID string, expiresAt time.Time) error
	userBySession(ctx context.Context, token string) (user, error)
	deleteSession(ctx context.Context, token string) error
}

type embeddingGenerator interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

type answerGenerator interface {
	GenerateAnswer(ctx context.Context, query string, passages []generation.Passage) (string, error)
	GradeAnswer(ctx context.Context, question, expectedAnswer, sourcePassage, learnerAnswer string) (generation.Grade, error)
}

type jobPublisher interface {
	Publish(ctx context.Context, jobID string) error
}

type handler struct {
	store     documentStore
	embedder  embeddingGenerator
	generator answerGenerator
	publisher jobPublisher
	extractor textExtractor
	splitter  chunking.WordSplitter
	logger    *slog.Logger
}

type submitDocumentRequest struct {
	Content string `json:"content"`
	Title   string `json:"title"`
}

type submitDocumentResponse struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status"`
}

const (
	sourceText = "text"
	sourcePDF  = "pdf"
)

type newDocument struct {
	Title      string
	SourceType string
	Content    string
	UserID     string
}

type document struct {
	ID          string    `json:"id"`
	Title       string    `json:"title,omitempty"`
	SourceType  string    `json:"source_type"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason,omitempty"`
	CardsStatus string    `json:"cards_status"`
	CardCount   int       `json:"card_count"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func NewHandler(store documentStore, embedder embeddingGenerator, generator answerGenerator, publisher jobPublisher, extractor textExtractor) http.Handler {
	splitter, err := chunking.NewWordSplitter(defaultChunkMaxWords, defaultChunkOverlapWords)
	if err != nil {
		panic(err)
	}

	h := &handler{store: store, embedder: embedder, generator: generator, publisher: publisher, extractor: extractor, splitter: splitter, logger: slog.Default()}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("POST /auth/register", h.register)
	mux.HandleFunc("POST /auth/login", h.login)
	mux.HandleFunc("POST /auth/logout", h.logout)
	mux.HandleFunc("GET /auth/me", h.requireUser(h.currentUser))
	mux.HandleFunc("POST /documents", h.requireUser(h.submitDocument))
	mux.HandleFunc("POST /documents/upload", h.requireUser(h.uploadDocument))
	mux.HandleFunc("GET /documents", h.requireUser(h.listDocuments))
	mux.HandleFunc("GET /documents/{id}", h.requireUser(h.getDocument))
	mux.HandleFunc("DELETE /documents/{id}", h.requireUser(h.deleteDocument))
	mux.HandleFunc("GET /chunks/{id}/context", h.requireUser(h.chunkContext))
	mux.HandleFunc("POST /search", h.requireUser(h.search))
	mux.HandleFunc("POST /answer", h.requireUser(h.answer))
	mux.HandleFunc("GET /reviews/due", h.requireUser(h.dueReviews))
	mux.HandleFunc("POST /reviews/{card_id}", h.requireUser(h.submitReview))
	mux.Handle("GET /", web.Handler())

	return h.logRequests(mux)
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

	title := strings.TrimSpace(request.Title)
	if utf8.RuneCountInString(title) > maxTitleCharacters {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "title must not exceed 200 characters"})
		return
	}

	account, _ := userFromContext(r.Context())
	chunks := h.splitter.Split(request.Content)
	if h.writeQuotaError(w, account, h.withinQuota(r.Context(), account, documentPages(chunks))) {
		return
	}

	doc := newDocument{Title: title, SourceType: sourceText, Content: request.Content, UserID: account.ID}

	id, jobID, err := h.store.createDocument(r.Context(), doc, chunks)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not store document"})
		return
	}

	h.notify(r.Context(), jobID)

	writeJSON(w, http.StatusAccepted, submitDocumentResponse{ID: id, Title: title, Status: statusQueued})
}

func (h *handler) notify(ctx context.Context, jobID string) {
	if h.publisher == nil {
		return
	}

	if err := h.publisher.Publish(ctx, jobID); err != nil && h.logger != nil {
		h.log(ctx).Error("publish job", "job_id", jobID, "error", err)
	}
}

func (h *handler) getDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "document id must be a valid UUID"})
		return
	}

	account, _ := userFromContext(r.Context())
	result, err := h.store.getDocument(r.Context(), id, account.ID)
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
