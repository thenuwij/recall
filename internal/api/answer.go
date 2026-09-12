package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/thenujawijesuriya/recall/internal/embedding"
	"github.com/thenujawijesuriya/recall/internal/generation"
)

const (
	defaultAnswerLimit  = 5
	maxAnswerLimit      = 10
	minAnswerSimilarity = 0.25
)

type answerRequest struct {
	Query string `json:"query"`
	Limit *int   `json:"limit"`
}

type citation struct {
	Marker     int     `json:"marker"`
	ChunkID    string  `json:"chunk_id"`
	DocumentID string  `json:"document_id"`
	Title      string  `json:"title,omitempty"`
	ChunkIndex int     `json:"chunk_index"`
	Page       *int    `json:"page,omitempty"`
	Content    string  `json:"content"`
	Similarity float64 `json:"similarity"`
}

type answerResponse struct {
	Answer    string     `json:"answer"`
	Citations []citation `json:"citations"`
	Refused   bool       `json:"refused"`
	Reason    string     `json:"reason,omitempty"`
}

var citationMarkerPattern = regexp.MustCompile(`\[(\d+)\]`)

func (h *handler) answer(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var request answerRequest
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

	limit := defaultAnswerLimit
	if request.Limit != nil {
		limit = *request.Limit
		if limit < 1 || limit > maxAnswerLimit {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "limit must be between 1 and 10"})
			return
		}
	}

	embeddings, err := h.embedder.Embed(r.Context(), []string{query})
	if err != nil || len(embeddings) != 1 || len(embeddings[0]) != embedding.Dimensions {
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "could not generate query embedding"})
		return
	}

	account, _ := userFromContext(r.Context())
	results, err := h.store.searchChunks(r.Context(), embeddings[0], embedding.Model, limit, account.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not search documents"})
		return
	}

	if len(results) == 0 {
		writeRefusal(w, "no documents matched the question")
		return
	}
	if results[0].Similarity < minAnswerSimilarity {
		writeRefusal(w, "no sufficiently relevant evidence was found")
		return
	}

	passages := make([]generation.Passage, 0, len(results))
	for index, result := range results {
		passages = append(passages, generation.Passage{Marker: index + 1, Content: result.Content})
	}

	generated, err := h.generator.GenerateAnswer(r.Context(), query, passages)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "could not generate an answer"})
		return
	}

	generated = strings.TrimSpace(generated)
	markers, err := citedMarkers(generated, len(passages))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "generated answer cited evidence that was not provided"})
		return
	}

	if generated == "" || len(markers) == 0 {
		writeRefusal(w, "the evidence did not support an answer")
		return
	}

	citations := make([]citation, 0, len(markers))
	for _, marker := range markers {
		result := results[marker-1]
		citations = append(citations, citation{
			Marker:     marker,
			ChunkID:    result.ChunkID,
			DocumentID: result.DocumentID,
			Title:      result.Title,
			ChunkIndex: result.ChunkIndex,
			Page:       result.Page,
			Content:    result.Content,
			Similarity: result.Similarity,
		})
	}

	writeJSON(w, http.StatusOK, answerResponse{Answer: generated, Citations: citations})
}

func writeRefusal(w http.ResponseWriter, reason string) {
	writeJSON(w, http.StatusOK, answerResponse{
		Citations: make([]citation, 0),
		Refused:   true,
		Reason:    reason,
	})
}

func citedMarkers(answer string, available int) ([]int, error) {
	matches := citationMarkerPattern.FindAllStringSubmatch(answer, -1)
	seen := make(map[int]bool, len(matches))
	markers := make([]int, 0, len(matches))

	for _, match := range matches {
		marker, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("unparsable citation marker %q", match[1])
		}
		if marker < 1 || marker > available {
			return nil, fmt.Errorf("citation marker %d is outside the %d provided passages", marker, available)
		}
		if seen[marker] {
			continue
		}
		seen[marker] = true
		markers = append(markers, marker)
	}

	sort.Ints(markers)
	return markers, nil
}
