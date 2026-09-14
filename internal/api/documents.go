package api

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"uuid"
)

const textContextBytes = 500

var (
	errChunkNotFound     = errors.New("chunk not found")
	errNoSourceLocation  = errors.New("this document predates source locations; re-upload it")
	errInvalidSourceSpan = errors.New("stored source location is invalid")
)

type documentSummary struct {
	ID          string    `json:"id"`
	Title       string    `json:"title,omitempty"`
	SourceType  string    `json:"source_type"`
	Locked      bool      `json:"locked"`
	Status      string    `json:"status"`
	CardsStatus string    `json:"cards_status"`
	CardCount   int       `json:"card_count"`
	CreatedAt   time.Time `json:"created_at"`
}

type documentListResponse struct {
	Documents []documentSummary `json:"documents"`
}

type chunkLocation struct {
	DocumentID string
	Title      string
	SourceType string
	Content    string
	Page       *int
	Start      *int
	End        *int
}

type passageContextResponse struct {
	DocumentID string `json:"document_id"`
	Title      string `json:"title,omitempty"`
	Page       *int   `json:"page,omitempty"`
	Before     string `json:"before"`
	Passage    string `json:"passage"`
	After      string `json:"after"`
}

func (h *handler) listDocuments(w http.ResponseWriter, r *http.Request) {
	account, _ := userFromContext(r.Context())
	documents, err := h.store.listDocuments(r.Context(), account.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not list documents"})
		return
	}

	if documents == nil {
		documents = make([]documentSummary, 0)
	}

	writeJSON(w, http.StatusOK, documentListResponse{Documents: documents})
}

func (h *handler) deleteDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "document id must be a valid UUID"})
		return
	}

	account, _ := userFromContext(r.Context())
	err := h.store.deleteDocument(r.Context(), id, account.ID)
	if errors.Is(err, errDocumentNotFound) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "document not found"})
		return
	}
	if errors.Is(err, errDocumentLocked) {
		writeJSON(w, http.StatusForbidden, errorResponse{Error: "this document is locked and cannot be deleted"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not delete document"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) chunkContext(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "chunk id must be a valid UUID"})
		return
	}

	account, _ := userFromContext(r.Context())
	location, err := h.store.chunkLocation(r.Context(), id, account.ID)
	if errors.Is(err, errChunkNotFound) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "chunk not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not retrieve chunk"})
		return
	}

	response, err := buildPassageContext(location)
	if errors.Is(err, errNoSourceLocation) {
		writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func buildPassageContext(location chunkLocation) (passageContextResponse, error) {
	if location.Start == nil || location.End == nil {
		return passageContextResponse{}, errNoSourceLocation
	}

	start, end := *location.Start, *location.End
	if start < 0 || start >= end || end > len(location.Content) {
		return passageContextResponse{}, errInvalidSourceSpan
	}

	before, passage, after := passageContext(location.Content, start, end, location.SourceType == sourcePDF)
	return passageContextResponse{
		DocumentID: location.DocumentID,
		Title:      location.Title,
		Page:       location.Page,
		Before:     before,
		Passage:    passage,
		After:      after,
	}, nil
}

func passageContext(content string, start, end int, paged bool) (string, string, string) {
	from, to := 0, len(content)

	if paged {
		if index := strings.LastIndexByte(content[:start], '\f'); index >= 0 {
			from = index + 1
		}
		if index := strings.IndexByte(content[end:], '\f'); index >= 0 {
			to = end + index
		}
		return content[from:start], content[start:end], content[end:to]
	}

	if start > textContextBytes {
		from = start - textContextBytes
		if index := strings.IndexFunc(content[from:start], unicode.IsSpace); index >= 0 {
			from += index
		} else {
			from = start
		}
	}
	if len(content)-end > textContextBytes {
		to = end + textContextBytes
		if index := strings.LastIndexFunc(content[end:to], unicode.IsSpace); index >= 0 {
			to = end + index
		} else {
			to = end
		}
	}

	return content[from:start], content[start:end], content[end:to]
}
