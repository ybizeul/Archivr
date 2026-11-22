package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/storage"
	"github.com/archivr/backend/internal/store"
)

// pathID parses the {id} path segment. It writes a 400 and returns false when it is not a number.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "id must be a positive number")
		return 0, false
	}
	return id, true
}

// optionalID parses an optional numeric query parameter. Missing or empty means 0.
func optionalID(raw string) int64 {
	id, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return id
}

func intParam(raw string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

// writeStoreError maps domain errors to HTTP answers. Every write handler uses it,
// so the codes stay the same across endpoints.
func (s *server) writeStoreError(w http.ResponseWriter, err error) {
	var dup *archive.DuplicateError
	switch {
	case errors.As(err, &dup):
		writeError(w, http.StatusConflict, "DUPLICATE",
			"this file is already filed as No. "+strconv.FormatInt(dup.Document.ID, 10))
	case errors.Is(err, archive.ErrInvalid):
		writeError(w, http.StatusBadRequest, "INVALID", strings.TrimPrefix(err.Error(), archive.ErrInvalid.Error()+": "))
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "not found")
	case errors.Is(err, store.ErrInUse):
		writeError(w, http.StatusConflict, "IN_USE", strings.TrimPrefix(err.Error(), store.ErrInUse.Error()+": "))
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "CONFLICT", "an item with this name already exists")
	case errors.Is(err, storage.ErrExists):
		writeError(w, http.StatusConflict, "FILE_EXISTS", "a file with this name already exists on disk")
	case errors.Is(err, storage.ErrInvalidName), errors.Is(err, storage.ErrNotEmpty):
		writeError(w, http.StatusBadRequest, "INVALID", err.Error())
	default:
		s.Log.Error("request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "something went wrong. The details are in the server log")
	}
}

// decodeBody reads a JSON body, writing the error answer on failure.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := readJSON(w, r, v); err != nil {
		writeBodyError(w, err)
		return false
	}
	return true
}

// writeBodyError answers a body that readJSON refused: 415 when it is not JSON, else 400.
func writeBodyError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotJSON) {
		writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
}

// nonNil keeps empty lists as [] in JSON, never null.
func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}
