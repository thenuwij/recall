package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/jackc/pgx/v5"
)

type folder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type libraryStore interface {
	listFolders(context.Context, string) ([]folder, error)
	saveFolder(context.Context, string, string, string) (folder, error)
	deleteFolder(context.Context, string, string) error
	moveDocument(context.Context, string, string, string) error
	originalPDF(context.Context, string, string) ([]byte, error)
}

var errFolderNotFound = errors.New("folder not found")

func (s *PostgresStore) listFolders(ctx context.Context, userID string) ([]folder, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, name FROM folders WHERE user_id=$1 ORDER BY lower(name), id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []folder{}
	for rows.Next() {
		var f folder
		if err := rows.Scan(&f.ID, &f.Name); err != nil {
			return nil, err
		}
		result = append(result, f)
	}
	return result, rows.Err()
}
func (s *PostgresStore) saveFolder(ctx context.Context, id, userID, name string) (folder, error) {
	var f folder
	var err error
	if id == "" {
		err = s.pool.QueryRow(ctx, `INSERT INTO folders(user_id,name) VALUES ($1,$2) RETURNING id::text,name`, userID, name).Scan(&f.ID, &f.Name)
	} else {
		err = s.pool.QueryRow(ctx, `UPDATE folders SET name=$3 WHERE id=$1 AND user_id=$2 RETURNING id::text,name`, id, userID, name).Scan(&f.ID, &f.Name)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return f, errFolderNotFound
	}
	return f, err
}
func (s *PostgresStore) deleteFolder(ctx context.Context, id, userID string) error {
	result, err := s.pool.Exec(ctx, `DELETE FROM folders WHERE id=$1 AND user_id=$2`, id, userID)
	if err == nil && result.RowsAffected() == 0 {
		return errFolderNotFound
	}
	return err
}
func (s *PostgresStore) moveDocument(ctx context.Context, id, userID, folderID string) error {
	if folderID != "" {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM folders WHERE id=$1 AND user_id=$2)`, folderID, userID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errFolderNotFound
		}
	}
	result, err := s.pool.Exec(ctx, `UPDATE documents SET folder_id=NULLIF($3,'')::uuid WHERE id=$1 AND user_id=$2 AND NOT locked`, id, userID, folderID)
	if err == nil && result.RowsAffected() == 0 {
		return errDocumentNotFound
	}
	return err
}
func (s *PostgresStore) originalPDF(ctx context.Context, id, userID string) ([]byte, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT f.data FROM document_files f JOIN documents d ON d.id=f.document_id WHERE d.id=$1 AND d.user_id=$2`, id, userID).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errDocumentNotFound
	}
	return data, err
}
func validID(id string) bool { _, err := uuid.Parse(id); return err == nil }
func libraryError(w http.ResponseWriter, err error) {
	if errors.Is(err, errFolderNotFound) || errors.Is(err, errDocumentNotFound) {
		writeJSON(w, 404, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 500, errorResponse{Error: "could not update the library"})
}
func decodeLibrary(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		writeJSON(w, 400, errorResponse{Error: "invalid request"})
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, 400, errorResponse{Error: "request must contain one JSON object"})
		return false
	}
	return true
}
func (h *handler) folders(w http.ResponseWriter, r *http.Request) {
	account, _ := userFromContext(r.Context())
	if r.Method == http.MethodGet {
		items, err := h.store.listFolders(r.Context(), account.ID)
		if err != nil {
			libraryError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"folders": items})
		return
	}
	// Shared demo accounts remain stable for the next visitor.
	if account.IsDemo {
		writeJSON(w, 403, errorResponse{Error: "Create an account to organise your own folders."})
		return
	}
	id := r.PathValue("id")
	if id != "" && !validID(id) {
		writeJSON(w, 400, errorResponse{Error: "invalid folder id"})
		return
	}
	if r.Method == http.MethodDelete {
		if err := h.store.deleteFolder(r.Context(), id, account.ID); err != nil {
			libraryError(w, err)
			return
		}
		w.WriteHeader(204)
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !decodeLibrary(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 80 {
		writeJSON(w, 400, errorResponse{Error: "Folder names must be between 1 and 80 characters."})
		return
	}
	f, err := h.store.saveFolder(r.Context(), id, account.ID, input.Name)
	if err != nil {
		libraryError(w, err)
		return
	}
	writeJSON(w, 200, f)
}
func (h *handler) moveDocument(w http.ResponseWriter, r *http.Request) {
	account, _ := userFromContext(r.Context())
	if account.IsDemo {
		writeJSON(w, 403, errorResponse{Error: "Create an account to organise your own folders."})
		return
	}
	var input struct {
		FolderID string `json:"folder_id"`
	}
	if !decodeLibrary(w, r, &input) {
		return
	}
	if !validID(r.PathValue("id")) || (input.FolderID != "" && !validID(input.FolderID)) {
		writeJSON(w, 400, errorResponse{Error: "invalid document or folder id"})
		return
	}
	if err := h.store.moveDocument(r.Context(), r.PathValue("id"), account.ID, input.FolderID); err != nil {
		libraryError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *handler) pdf(w http.ResponseWriter, r *http.Request) {
	if !validID(r.PathValue("id")) {
		writeJSON(w, 400, errorResponse{Error: "invalid document id"})
		return
	}
	account, _ := userFromContext(r.Context())
	data, err := h.store.originalPDF(r.Context(), r.PathValue("id"), account.ID)
	if err != nil {
		libraryError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="recall-document.pdf"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "document.pdf", time.Time{}, bytes.NewReader(data))
}
