package extraction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func realExtractor(t *testing.T) *PDFExtractor {
	t.Helper()

	extractor, err := NewPDFExtractor()
	if errors.Is(err, ErrMissingTool) {
		t.Skip("pdftotext is not installed")
	}
	if err != nil {
		t.Fatalf("create extractor: %v", err)
	}
	return extractor
}

func fakeExtractor(t *testing.T, script string, timeout time.Duration) *PDFExtractor {
	t.Helper()

	path := filepath.Join(t.TempDir(), "pdftotext")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write fake tool: %v", err)
	}
	return &PDFExtractor{binary: path, timeout: timeout}
}

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()

	file, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

const fakePDF = "%PDF-1.4 fake body"

func TestNewPDFExtractorRequiresTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if _, err := NewPDFExtractor(); !errors.Is(err, ErrMissingTool) {
		t.Fatalf("error = %v, want %v", err, ErrMissingTool)
	}
}

func TestExtractRejectsNonPDF(t *testing.T) {
	extractor := &PDFExtractor{binary: "/nonexistent/pdftotext", timeout: time.Second}

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "shorter than the magic", input: "%PD"},
		{name: "plain text", input: "The cardiac cycle has two phases."},
		{name: "text renamed to pdf", input: "PDF-1.4 but no percent sign"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := extractor.Extract(context.Background(), strings.NewReader(tt.input)); !errors.Is(err, ErrNotPDF) {
				t.Fatalf("error = %v, want %v", err, ErrNotPDF)
			}
		})
	}
}

func TestExtractPropagatesReadErrors(t *testing.T) {
	extractor := &PDFExtractor{binary: "/nonexistent/pdftotext", timeout: time.Second}
	readError := errors.New("connection reset")

	_, err := extractor.Extract(context.Background(), iotest.ErrReader(readError))
	if !errors.Is(err, readError) {
		t.Fatalf("error = %v, want it to wrap %v", err, readError)
	}
	if errors.Is(err, ErrNotPDF) {
		t.Fatal("a failed read was reported as a non-PDF file")
	}
}

func TestExtractKeepsPageBreaks(t *testing.T) {
	extractor := realExtractor(t)

	text, err := extractor.Extract(context.Background(), openFixture(t, "pages.pdf"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	pages := strings.Split(text, "\f")
	for index := range pages {
		pages[index] = strings.TrimSpace(pages[index])
	}
	want := []string{
		"The cardiac cycle has two phases.",
		"Both valves stay closed during isovolumetric contraction.",
		"",
		"Ventricular pressure opens the aortic valve.",
		"",
	}
	if !reflect.DeepEqual(pages, want) {
		t.Fatalf("pages = %#v, want %#v", pages, want)
	}
}

func TestExtractRejectsPDFWithoutText(t *testing.T) {
	extractor := realExtractor(t)

	if _, err := extractor.Extract(context.Background(), openFixture(t, "blank.pdf")); !errors.Is(err, ErrNoText) {
		t.Fatalf("error = %v, want %v", err, ErrNoText)
	}
}

func TestExtractMapsToolFailures(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   error
	}{
		{name: "password protected", script: "echo 'Command Line Error: Incorrect password' >&2; exit 1", want: ErrEncrypted},
		{name: "damaged file", script: "echo 'Syntax Error: Couldn'\\''t find trailer dictionary' >&2; exit 1", want: ErrUnreadable},
		{name: "whitespace output", script: "printf ' \\f\\n'", want: ErrNoText},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := fakeExtractor(t, tt.script, 5*time.Second)
			if _, err := extractor.Extract(context.Background(), strings.NewReader(fakePDF)); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestExtractTimesOut(t *testing.T) {
	extractor := fakeExtractor(t, "exec sleep 5", 50*time.Millisecond)

	started := time.Now()
	_, err := extractor.Extract(context.Background(), strings.NewReader(fakePDF))
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want %v", err, ErrTimeout)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("extraction took %s after the timeout", elapsed)
	}
}

func TestExtractHonoursCancelledContext(t *testing.T) {
	extractor := fakeExtractor(t, "exec sleep 5", 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := extractor.Extract(ctx, strings.NewReader(fakePDF)); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want %v", err, context.Canceled)
	}
}

func TestExtractRemovesTemporaryFile(t *testing.T) {
	tests := []struct {
		name   string
		script string
	}{
		{name: "success", script: "printf 'page one\\f'"},
		{name: "failure", script: "echo 'Syntax Error' >&2; exit 1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			temporary := t.TempDir()
			t.Setenv("TMPDIR", temporary)
			extractor := fakeExtractor(t, tt.script, 5*time.Second)

			_, _ = extractor.Extract(context.Background(), strings.NewReader(fakePDF))

			entries, err := os.ReadDir(temporary)
			if err != nil {
				t.Fatalf("read temporary directory: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("temporary directory has %d entries after extraction, want 0", len(entries))
			}
		})
	}
}

func TestExtractPassesWholeFileToTool(t *testing.T) {
	extractor := fakeExtractor(t, `cat "$3"`, 5*time.Second)

	text, err := extractor.Extract(context.Background(), strings.NewReader(fakePDF))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if text != fakePDF {
		t.Fatalf("tool received %q, want the complete upload %q", text, fakePDF)
	}
}
