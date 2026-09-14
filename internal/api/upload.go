package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/thenujawijesuriya/recall/internal/extraction"
)

const (
	maxUploadFileBytes      = 25 << 20
	maxUploadOverheadBytes  = 1 << 20
	maxTitleCharacters      = 200
	uploadFileField         = "file"
	uploadTooLargeMessage   = "file must not exceed 25 MiB"
	unsupportedTypeMessage  = "only .pdf, .txt and .md files are supported"
	invalidTextFileMessage  = "text files must be UTF-8 without NUL bytes"
	missingFileFieldMessage = "request must be multipart/form-data with a file field"
)

var errUploadTooLarge = errors.New(uploadTooLargeMessage)

type textExtractor interface {
	Extract(ctx context.Context, r io.Reader) (string, error)
}

type uploadResponse struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status"`
}

func (h *handler) uploadDocument(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadFileBytes+maxUploadOverheadBytes)

	reader, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: missingFileFieldMessage})
		return
	}

	filename, data, err := readUploadedFile(reader)
	if errors.Is(err, errUploadTooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: uploadTooLargeMessage})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: missingFileFieldMessage})
		return
	}

	extension := strings.ToLower(filepath.Ext(filename))
	account, _ := userFromContext(r.Context())
	doc := newDocument{Title: titleFromFilename(filename), UserID: account.ID}

	switch extension {
	case ".txt", ".md", ".markdown":
		doc.SourceType = sourceText
		doc.Content = string(data)
	case ".pdf":
		text, err := h.extractor.Extract(r.Context(), bytes.NewReader(data))
		if err != nil {
			h.writeExtractionError(w, r.Context(), err)
			return
		}
		doc.PDF = data
		doc.SourceType = sourcePDF
		doc.Content = text
	default:
		writeJSON(w, http.StatusUnsupportedMediaType, errorResponse{Error: unsupportedTypeMessage})
		return
	}

	if !utf8.ValidString(doc.Content) || strings.ContainsRune(doc.Content, 0) {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: invalidTextFileMessage})
		return
	}

	chunks := h.splitter.Split(doc.Content)
	if len(chunks) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "file must not be empty"})
		return
	}

	if h.writeQuotaError(w, account, h.withinQuota(r.Context(), account, documentPages(chunks))) {
		return
	}

	id, jobID, err := h.store.createDocument(r.Context(), doc, chunks)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not store document"})
		return
	}

	h.notify(r.Context(), jobID)

	writeJSON(w, http.StatusAccepted, uploadResponse{ID: id, Title: doc.Title, Status: statusQueued})
}

func readUploadedFile(reader *multipart.Reader) (string, []byte, error) {
	for {
		part, err := reader.NextPart()
		if err != nil {
			return "", nil, err
		}

		if part.FormName() != uploadFileField {
			_ = part.Close()
			continue
		}

		data, err := io.ReadAll(io.LimitReader(part, maxUploadFileBytes+1))
		_ = part.Close()
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) || len(data) > maxUploadFileBytes {
			return "", nil, errUploadTooLarge
		}
		if err != nil {
			return "", nil, err
		}

		return part.FileName(), data, nil
	}
}

func titleFromFilename(filename string) string {
	base := filepath.Base(strings.ReplaceAll(filename, `\`, "/"))
	if base == "." || base == "/" {
		return ""
	}

	title := strings.TrimSpace(strings.TrimSuffix(base, filepath.Ext(base)))
	if utf8.RuneCountInString(title) > maxTitleCharacters {
		title = string([]rune(title)[:maxTitleCharacters])
	}
	return title
}

func (h *handler) writeExtractionError(w http.ResponseWriter, ctx context.Context, err error) {
	switch {
	case ctx.Err() != nil:
		return
	case errors.Is(err, extraction.ErrNotPDF):
		writeJSON(w, http.StatusUnsupportedMediaType, errorResponse{Error: "file is not a valid PDF"})
	case errors.Is(err, extraction.ErrNoText), errors.Is(err, extraction.ErrEncrypted):
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
	case errors.Is(err, extraction.ErrUnreadable):
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: extraction.ErrUnreadable.Error()})
	case errors.Is(err, extraction.ErrTimeout):
		writeJSON(w, http.StatusGatewayTimeout, errorResponse{Error: err.Error()})
	default:
		if h.logger != nil {
			h.log(ctx).Error("extract pdf", "error", err)
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not extract text from PDF"})
	}
}
