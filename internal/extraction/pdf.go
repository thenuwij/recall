package extraction

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const defaultTimeout = 30 * time.Second

var (
	ErrMissingTool = errors.New("pdftotext is not installed")
	ErrNotPDF      = errors.New("file is not a PDF")
	ErrNoText      = errors.New("PDF contains no extractable text; scanned PDFs are not supported")
	ErrEncrypted   = errors.New("PDF is password protected")
	ErrTimeout     = errors.New("PDF text extraction timed out")
	ErrUnreadable  = errors.New("PDF could not be read")
)

var pdfMagic = []byte("%PDF-")

type PDFExtractor struct {
	binary  string
	timeout time.Duration
}

func NewPDFExtractor() (*PDFExtractor, error) {
	binary, err := exec.LookPath("pdftotext")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMissingTool, err)
	}

	return &PDFExtractor{binary: binary, timeout: defaultTimeout}, nil
}

func (e *PDFExtractor) Extract(ctx context.Context, r io.Reader) (string, error) {
	header := make([]byte, len(pdfMagic))
	if _, err := io.ReadFull(r, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return "", ErrNotPDF
		}
		return "", fmt.Errorf("read upload: %w", err)
	}
	if !bytes.Equal(header, pdfMagic) {
		return "", ErrNotPDF
	}

	path, err := writeTemporary(io.MultiReader(bytes.NewReader(header), r))
	if path != "" {
		defer os.Remove(path)
	}
	if err != nil {
		return "", err
	}

	runContext, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(runContext, e.binary, "-enc", "UTF-8", path, "-")
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.WaitDelay = time.Second

	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.Is(runContext.Err(), context.DeadlineExceeded) {
			return "", ErrTimeout
		}
		message := strings.TrimSpace(stderr.String())
		if strings.Contains(strings.ToLower(message), "password") {
			return "", ErrEncrypted
		}
		return "", fmt.Errorf("%w: %s", ErrUnreadable, message)
	}

	text := stdout.String()
	if strings.TrimSpace(text) == "" {
		return "", ErrNoText
	}

	return text, nil
}

func writeTemporary(r io.Reader) (string, error) {
	file, err := os.CreateTemp("", "recall-upload-*.pdf")
	if err != nil {
		return "", fmt.Errorf("create temporary file: %w", err)
	}

	if _, err := io.Copy(file, r); err != nil {
		_ = file.Close()
		return file.Name(), fmt.Errorf("write temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return file.Name(), fmt.Errorf("close temporary file: %w", err)
	}

	return file.Name(), nil
}
