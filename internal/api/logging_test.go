package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLoggingRecordsMethodPathAndStatus(t *testing.T) {
	var buffer bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, nil)))
	handler := NewHandler(newMemoryStore(), &fakeEmbedder{}, &fakeGenerator{}, &fakePublisher{}, &fakeExtractor{})

	request := httptest.NewRequest(http.MethodGet, "/documents", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	output := buffer.String()
	for _, want := range []string{`"msg":"request"`, `"method":"GET"`, `"path":"/documents"`, `"status":401`, `"request_id"`, `"duration_ms"`} {
		if !strings.Contains(output, want) {
			t.Errorf("log missing %q; got:\n%s", want, output)
		}
	}
}

func TestRequestLoggingGivesEachRequestItsOwnID(t *testing.T) {
	var buffer bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, nil)))
	handler := NewHandler(newMemoryStore(), &fakeEmbedder{}, &fakeGenerator{}, &fakePublisher{}, &fakeExtractor{})

	for range 2 {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	}

	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want 2:\n%s", len(lines), buffer.String())
	}
	if lines[0] == lines[1] {
		t.Fatal("both requests logged identical lines, want distinct request ids")
	}
}
