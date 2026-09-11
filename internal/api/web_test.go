package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestWebInterfaceIsServed(t *testing.T) {
	tests := []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/", contentType: "text/html", contains: `src="/review.js"`},
		{path: "/review.js", contentType: "text/javascript", contains: "/reviews/due"},
		{path: "/library.html", contentType: "text/html", contains: `src="/library.js"`},
		{path: "/ask.html", contentType: "text/html", contains: `src="/ask.js"`},
		{path: "/app.js", contentType: "text/javascript", contains: "export async function api"},
		{path: "/style.css", contentType: "text/css", contains: "--accent"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			response := serve(newMemoryStore(), http.MethodGet, tt.path)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, tt.contentType) {
				t.Fatalf("content type = %q, want %q", got, tt.contentType)
			}
			if !strings.Contains(response.Body.String(), tt.contains) {
				t.Fatalf("body does not contain %q", tt.contains)
			}
		})
	}
}

func TestAPIRoutesTakePrecedenceOverWebInterface(t *testing.T) {
	response := serve(newMemoryStore(), http.MethodGet, "/documents")

	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
}

func TestUnknownWebPathIsNotFound(t *testing.T) {
	if response := serve(newMemoryStore(), http.MethodGet, "/missing.html"); response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}
