package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thenujawijesuriya/recall/internal/chunking"
	"github.com/thenujawijesuriya/recall/internal/extraction"
)

type fakeExtractor struct {
	text     string
	err      error
	calls    int
	received []byte
}

func (e *fakeExtractor) Extract(_ context.Context, r io.Reader) (string, error) {
	e.calls++
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	e.received = data
	if e.err != nil {
		return "", e.err
	}
	return e.text, nil
}

func uploadRequest(t *testing.T, field, filename string, content []byte) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("note", "ignored"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	part, err := writer.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/documents/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func serveUpload(store *memoryStore, extractor *fakeExtractor, publisher *fakePublisher, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	NewHandler(store, &fakeEmbedder{}, &fakeGenerator{}, publisher, extractor).ServeHTTP(response, request)
	return response
}

func TestUploadTextDocument(t *testing.T) {
	for _, filename := range []string{"Lecture 3.txt", "Lecture 3.md", "LECTURE 3.MD"} {
		t.Run(filename, func(t *testing.T) {
			store := newMemoryStore()
			extractor := &fakeExtractor{}
			publisher := &fakePublisher{}

			response := serveUpload(store, extractor, publisher, uploadRequest(t, "file", filename, []byte("The cardiac cycle\nhas two phases.")))

			if response.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusAccepted, response.Body.String())
			}
			var result uploadResponse
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			wantTitle := strings.TrimSuffix(filename, filename[strings.LastIndex(filename, "."):])
			if result.Title != wantTitle || result.Status != statusQueued {
				t.Fatalf("response = %+v, want title %q and status %q", result, wantTitle, statusQueued)
			}
			if extractor.calls != 0 {
				t.Errorf("extractor calls = %d, want 0 for a text file", extractor.calls)
			}

			stored := store.documents[result.ID]
			if stored.SourceType != sourceText || stored.Content != "The cardiac cycle\nhas two phases." {
				t.Fatalf("stored document = %+v", stored)
			}
			want := []chunking.Chunk{{Text: "The cardiac cycle has two phases.", Start: 0, End: 33, Page: 1}}
			if got := store.chunks[result.ID]; len(got) != 1 || got[0] != want[0] {
				t.Fatalf("stored chunks = %+v, want %+v", got, want)
			}
			if len(publisher.published) != 1 {
				t.Errorf("published jobs = %d, want 1", len(publisher.published))
			}
		})
	}
}

func TestUploadPDFDocument(t *testing.T) {
	store := newMemoryStore()
	extractor := &fakeExtractor{text: "page one\fpage two\f"}
	pdf := []byte("%PDF-1.4 lecture slides")

	response := serveUpload(store, extractor, &fakePublisher{}, uploadRequest(t, "file", "Week 3/Cardiac Cycle.pdf", pdf))

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if !bytes.Equal(extractor.received, pdf) {
		t.Fatalf("extractor received %q, want the uploaded bytes", extractor.received)
	}

	stored := store.documents["test-document-id"]
	if stored.Title != "Cardiac Cycle" || stored.SourceType != sourcePDF || stored.Content != "page one\fpage two\f" {
		t.Fatalf("stored document = %+v", stored)
	}
	chunks := store.chunks["test-document-id"]
	if len(chunks) != 2 || chunks[0].Page != 1 || chunks[1].Page != 2 {
		t.Fatalf("stored chunks = %+v, want one chunk on each of pages 1 and 2", chunks)
	}
}

func TestUploadRejectsBadRequests(t *testing.T) {
	tests := []struct {
		name       string
		request    func(t *testing.T) *http.Request
		extractor  *fakeExtractor
		wantStatus int
		wantError  string
	}{
		{
			name: "not multipart",
			request: func(t *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodPost, "/documents/upload", strings.NewReader(`{"content":"x"}`))
			},
			wantStatus: http.StatusBadRequest,
			wantError:  missingFileFieldMessage,
		},
		{
			name:       "missing file field",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "attachment", "notes.txt", []byte("text")) },
			wantStatus: http.StatusBadRequest,
			wantError:  missingFileFieldMessage,
		},
		{
			name:       "unsupported extension",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "file", "slides.pptx", []byte("PK")) },
			wantStatus: http.StatusUnsupportedMediaType,
			wantError:  unsupportedTypeMessage,
		},
		{
			name:       "empty text file",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "file", "notes.txt", []byte(" \n\t")) },
			wantStatus: http.StatusBadRequest,
			wantError:  "file must not be empty",
		},
		{
			name:       "invalid UTF-8",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "file", "notes.txt", []byte{'a', 0xff, 'b'}) },
			wantStatus: http.StatusUnprocessableEntity,
			wantError:  invalidTextFileMessage,
		},
		{
			name:       "NUL byte",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "file", "notes.txt", []byte("a\x00b")) },
			wantStatus: http.StatusUnprocessableEntity,
			wantError:  invalidTextFileMessage,
		},
		{
			name:       "text renamed to pdf",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "file", "notes.pdf", []byte("plain text")) },
			extractor:  &fakeExtractor{err: extraction.ErrNotPDF},
			wantStatus: http.StatusUnsupportedMediaType,
			wantError:  "file is not a valid PDF",
		},
		{
			name:       "scanned pdf",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "file", "scan.pdf", []byte("%PDF-1.4")) },
			extractor:  &fakeExtractor{err: extraction.ErrNoText},
			wantStatus: http.StatusUnprocessableEntity,
			wantError:  extraction.ErrNoText.Error(),
		},
		{
			name:       "encrypted pdf",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "file", "locked.pdf", []byte("%PDF-1.4")) },
			extractor:  &fakeExtractor{err: extraction.ErrEncrypted},
			wantStatus: http.StatusUnprocessableEntity,
			wantError:  extraction.ErrEncrypted.Error(),
		},
		{
			name:       "damaged pdf",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "file", "broken.pdf", []byte("%PDF-1.4")) },
			extractor:  &fakeExtractor{err: fmt.Errorf("%w: Syntax Error", extraction.ErrUnreadable)},
			wantStatus: http.StatusUnprocessableEntity,
			wantError:  extraction.ErrUnreadable.Error(),
		},
		{
			name:       "extraction timeout",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "file", "huge.pdf", []byte("%PDF-1.4")) },
			extractor:  &fakeExtractor{err: extraction.ErrTimeout},
			wantStatus: http.StatusGatewayTimeout,
			wantError:  extraction.ErrTimeout.Error(),
		},
		{
			name:       "unexpected extraction failure",
			request:    func(t *testing.T) *http.Request { return uploadRequest(t, "file", "odd.pdf", []byte("%PDF-1.4")) },
			extractor:  &fakeExtractor{err: errors.New("disk full")},
			wantStatus: http.StatusInternalServerError,
			wantError:  "could not extract text from PDF",
		},
		{
			name: "file one byte over the limit",
			request: func(t *testing.T) *http.Request {
				return uploadRequest(t, "file", "big.pdf", append([]byte("%PDF-"), make([]byte, maxUploadFileBytes-4)...))
			},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantError:  uploadTooLargeMessage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			extractor := tt.extractor
			if extractor == nil {
				extractor = &fakeExtractor{text: "unused"}
			}

			response := serveUpload(store, extractor, &fakePublisher{}, tt.request(t))

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.wantStatus, response.Body.String())
			}
			var body errorResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if body.Error != tt.wantError {
				t.Fatalf("error = %q, want %q", body.Error, tt.wantError)
			}
			if len(store.documents) != 0 {
				t.Fatalf("documents stored = %d, want 0", len(store.documents))
			}
		})
	}
}

func TestUploadAcceptsFileAtExactLimit(t *testing.T) {
	store := newMemoryStore()
	extractor := &fakeExtractor{text: "extracted"}
	pdf := append([]byte("%PDF-"), make([]byte, maxUploadFileBytes-5)...)

	response := serveUpload(store, extractor, &fakePublisher{}, uploadRequest(t, "file", "exact.pdf", pdf))

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if len(extractor.received) != maxUploadFileBytes {
		t.Fatalf("extractor received %d bytes, want %d", len(extractor.received), maxUploadFileBytes)
	}
}

func TestUploadOverLimitNeverReachesExtractor(t *testing.T) {
	extractor := &fakeExtractor{text: "unused"}
	pdf := append([]byte("%PDF-"), make([]byte, maxUploadFileBytes)...)

	response := serveUpload(newMemoryStore(), extractor, &fakePublisher{}, uploadRequest(t, "file", "big.pdf", pdf))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
	if extractor.calls != 0 {
		t.Fatalf("extractor calls = %d, want 0", extractor.calls)
	}
}

func TestUploadHandlesStoreFailure(t *testing.T) {
	store := newMemoryStore()
	store.err = errors.New("database unavailable")

	response := serveUpload(store, &fakeExtractor{}, &fakePublisher{}, uploadRequest(t, "file", "notes.txt", []byte("text")))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}

func TestTitleFromFilename(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"Lecture 3.pdf", "Lecture 3"},
		{"notes.tar.md", "notes.tar"},
		{"../../etc/passwd.txt", "passwd"},
		{`C:\Users\me\Week 1.pdf`, "Week 1"},
		{"  spaced  .txt", "spaced"},
		{"", ""},
		{strings.Repeat("é", 250) + ".txt", strings.Repeat("é", 200)},
	}

	for _, tt := range tests {
		if got := titleFromFilename(tt.filename); got != tt.want {
			t.Errorf("titleFromFilename(%q) = %q, want %q", tt.filename, got, tt.want)
		}
	}
}

func TestSubmitDocumentStoresOptionalTitle(t *testing.T) {
	store := newMemoryStore()
	request := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(`{"content":"notes","title":"  Week 1  "}`))
	response := httptest.NewRecorder()

	newTestHandler(store).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if got := store.documents["test-document-id"].Title; got != "Week 1" {
		t.Fatalf("stored title = %q, want %q", got, "Week 1")
	}
	if !strings.Contains(response.Body.String(), `"title":"Week 1"`) {
		t.Fatalf("body = %s, want it to echo the title", response.Body.String())
	}
}

func TestSubmitDocumentRejectsLongTitle(t *testing.T) {
	body := `{"content":"notes","title":"` + strings.Repeat("a", maxTitleCharacters+1) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/documents", strings.NewReader(body))
	response := httptest.NewRecorder()

	newTestHandler(newMemoryStore()).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}
