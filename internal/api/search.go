package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/thenujawijesuriya/recall/internal/embedding"
)

const (
	defaultSearchLimit = 5
	maxSearchLimit     = 20
)

type searchRequest struct {
	Query string `json:"query"`
	Limit *int   `json:"limit"`
}

type searchResult struct {
	ChunkID    string  `json:"chunk_id"`
	DocumentID string  `json:"document_id"`
	Title      string  `json:"title,omitempty"`
	ChunkIndex int     `json:"chunk_index"`
	Page       *int    `json:"page,omitempty"`
	Content    string  `json:"content"`
	Similarity float64 `json:"similarity"`
}

type searchResponse struct {
	Results []searchResult `json:"results"`
}

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var request searchRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body must not exceed 1 MiB"})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body must contain valid JSON with a query field"})
		return
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "request body must contain exactly one JSON object"})
		return
	}

	query := strings.TrimSpace(request.Query)
	if query == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "query must not be empty"})
		return
	}

	limit := defaultSearchLimit
	if request.Limit != nil {
		limit = *request.Limit
		if limit < 1 || limit > maxSearchLimit {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "limit must be between 1 and 20"})
			return
		}
	}

	embeddings, err := h.embedder.Embed(r.Context(), []string{query})
	if err != nil || len(embeddings) != 1 || len(embeddings[0]) != embedding.Dimensions {
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "could not generate query embedding"})
		return
	}

	results, err := h.store.searchChunks(r.Context(), embeddings[0], embedding.Model, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not search documents"})
		return
	}

	if results == nil {
		results = make([]searchResult, 0)
	}

	writeJSON(w, http.StatusOK, searchResponse{Results: results})
}
