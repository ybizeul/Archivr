package api

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/ingest"
	"github.com/archivr/backend/internal/preview"
	"github.com/archivr/backend/internal/store"
	"github.com/archivr/backend/internal/suggest"
)

// maxUploadBytes caps one uploaded file. Scans are large, but not unbounded.
const maxUploadBytes = 100 << 20

// documentDetail is a document plus its extracted text, for the detail screen.
type documentDetail struct {
	store.Document
	Text string `json:"text"`
}

func (s *server) inboxCount(w http.ResponseWriter, r *http.Request, _ store.User) {
	n, err := s.Store.CountInFolder(r.Context(), archive.InboxID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": n, "folder_id": archive.InboxID})
}

var ocrStatuses = map[string]bool{"pending": true, "done": true, "failed": true, "skipped": true}

func (s *server) listDocuments(w http.ResponseWriter, r *http.Request, _ store.User) {
	q := r.URL.Query()
	f := store.DocFilter{
		Query:     strings.TrimSpace(q.Get("q")),
		FolderID:  optionalID(q.Get("folder")),
		Subtree:   q.Get("subtree") == "1",
		TypeID:    optionalID(q.Get("type")),
		Tag:       strings.TrimSpace(q.Get("tag")),
		From:      strings.TrimSpace(q.Get("from")),
		To:        strings.TrimSpace(q.Get("to")),
		OCRStatus: strings.TrimSpace(q.Get("ocr")),
		Sort:      strings.TrimSpace(q.Get("sort")),
		Limit:     intParam(q.Get("limit"), 50),
		Offset:    intParam(q.Get("offset"), 0),
	}
	if q.Get("inbox") == "1" {
		f.FolderID = archive.InboxID
	}
	// Trash is opened on purpose. The library and global search leave it out.
	if f.FolderID == 0 {
		trash, err := s.Store.FolderByPath(r.Context(), archive.TrashName)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.writeStoreError(w, err)
			return
		}
		if err == nil {
			f.ExcludeFolderID = trash.ID
		}
	}
	if f.From != "" && !archive.ValidDate(f.From) {
		writeError(w, http.StatusBadRequest, "INVALID", "from must be YYYY-MM-DD")
		return
	}
	if f.To != "" && !archive.ValidDate(f.To) {
		writeError(w, http.StatusBadRequest, "INVALID", "to must be YYYY-MM-DD")
		return
	}
	if f.OCRStatus != "" && !ocrStatuses[f.OCRStatus] {
		writeError(w, http.StatusBadRequest, "INVALID", "ocr must be pending, done, failed or skipped")
		return
	}

	docs, total, err := s.Store.ListDocuments(r.Context(), f)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  nonNil(docs),
		"total":  total,
		"limit":  f.Limit,
		"offset": f.Offset,
	})
}

func (s *server) getDocument(w http.ResponseWriter, r *http.Request, _ store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	doc, err := s.Store.GetDocument(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	text, err := s.Store.DocumentText(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, documentDetail{Document: doc, Text: text})
}

func (s *server) patchDocument(w http.ResponseWriter, r *http.Request, _ store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var p archive.Patch
	if !decodeBody(w, r, &p) {
		return
	}
	doc, err := s.Archive.Update(r.Context(), id, p)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *server) deleteDocument(w http.ResponseWriter, r *http.Request, _ store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Archive.Delete(r.Context(), id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// uploadDocument files one uploaded file into INBOX. Multipart fields: file, an optional title, and an
// optional valid_until (YYYY-MM-DD): the document moves to Trash once that day has passed.
func (s *server) uploadDocument(w http.ResponseWriter, r *http.Request, _ store.User) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "TOO_LARGE", "file is larger than 100 MB")
			return
		}
		writeError(w, http.StatusBadRequest, "INVALID", "expected a multipart form with a file field")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID", "choose a file to upload")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID", "could not read the file")
		return
	}

	var validUntil *string
	if v := strings.TrimSpace(r.FormValue("valid_until")); v != "" {
		if !archive.ValidDate(v) {
			writeError(w, http.StatusBadRequest, "INVALID", "valid_until must be YYYY-MM-DD")
			return
		}
		validUntil = &v
	}

	res, err := s.Ingest.Import(r.Context(), ingest.Candidate{
		ValidUntil: validUntil,
		Source:     "upload",
		SourceRef:  header.Filename + "|" + strconv.Itoa(len(data)),
		Filename:   header.Filename,
		Title:      r.FormValue("title"),
		Data:       data,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	switch res.Status {
	case ingest.Imported:
		writeJSON(w, http.StatusCreated, res.Document)
	case ingest.Duplicate:
		writeError(w, http.StatusConflict, "DUPLICATE",
			"this file is already filed as No. "+strconv.FormatInt(res.Document.ID, 10))
	case ingest.AlreadyImported:
		msg := "this file was already imported"
		if res.Document.ID != 0 {
			msg = "this file is already filed as No. " + strconv.FormatInt(res.Document.ID, 10)
		}
		writeError(w, http.StatusConflict, "DUPLICATE", msg)
	default:
		reason := res.Reason
		if reason == "" {
			reason = "this file type is not supported. Upload a PDF, PNG, JPEG, TIFF or WebP."
		}
		writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_TYPE", reason)
	}
}

// documentFile streams the stored file. It is served inline, so the browser can show it.
func (s *server) documentFile(w http.ResponseWriter, r *http.Request, _ store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	doc, err := s.Store.GetDocument(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	path, err := s.Archive.Layout.DocumentFile(doc.Folder.Path, doc.Filename)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.Log.Error("document file missing", "id", id, "path", path, "error", err)
		writeError(w, http.StatusNotFound, "FILE_MISSING", "the file is missing from the archive folder")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", doc.Mime)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": doc.Filename}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, doc.Filename, info.ModTime(), f)
}

func (s *server) documentThumbnail(w http.ResponseWriter, r *http.Request, _ store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	doc, err := s.Store.GetDocument(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	src, err := s.Archive.Layout.DocumentFile(doc.Folder.Path, doc.Filename)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	thumb, err := s.Previews.Thumbnail(r.Context(), doc.SHA256, src, doc.Mime)
	if errors.Is(err, preview.ErrUnavailable) {
		writeError(w, http.StatusNotFound, "THUMBNAIL_UNAVAILABLE", "no preview for this file")
		return
	}
	if err != nil {
		s.Log.Error("thumbnail failed", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "could not make a preview")
		return
	}
	// The cache key is the content hash, so a thumbnail never changes.
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	http.ServeFile(w, r, thumb)
}

// documentSuggestions proposes dates and titles from the OCR text. The user chooses.
func (s *server) documentSuggestions(w http.ResponseWriter, r *http.Request, _ store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.Store.GetDocument(r.Context(), id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	text, err := s.Store.DocumentText(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, suggest.Analyse(text))
}

func (s *server) reOCR(w http.ResponseWriter, r *http.Request, _ store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.Store.GetDocument(r.Context(), id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if err := s.OCR.Reprocess(r.Context(), id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	doc, err := s.Store.GetDocument(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, doc)
}
