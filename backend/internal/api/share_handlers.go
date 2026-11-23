package api

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/archivr/backend/internal/auth"
	"github.com/archivr/backend/internal/store"
)

type shareRequest struct {
	// ExpiresInSeconds is how long the link stays valid, from now.
	ExpiresInSeconds int64 `json:"expires_in_seconds"`
}

type shareResponse struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// sharedDocument is all a share link shows: no folder, tags, correspondent or OCR text.
type sharedDocument struct {
	Title        string  `json:"title"`
	DocumentDate *string `json:"document_date"`
	Filename     string  `json:"filename"`
	Mime         string  `json:"mime"`
	Size         int64   `json:"size"`
	Rotation     int     `json:"rotation"`
	ExpiresAt    string  `json:"expires_at"`
}

// createShare signs a link to one document, valid for the period the user chose.
// The link is a signed token, so nothing is stored: it works until it expires, or until
// the document is deleted or JWT_SECRET changes. Only a user who can see the document can
// share it. A private document can be shared by its owner: handing out the link is their choice.
func (s *server) createShare(w http.ResponseWriter, r *http.Request, user store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req shareRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.ExpiresInSeconds <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID", "choose how long the link stays valid")
		return
	}
	// Compare in seconds, so a huge value cannot overflow time.Duration.
	if req.ExpiresInSeconds < int64(auth.MinShareTTL/time.Second) || req.ExpiresInSeconds > int64(auth.MaxShareTTL/time.Second) {
		writeError(w, http.StatusBadRequest, "INVALID", "the link must stay valid between 1 minute and 365 days")
		return
	}
	if _, err := s.Store.GetDocumentFor(r.Context(), user.ID, id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	token, expires, err := s.Shares.Issue(id, time.Duration(req.ExpiresInSeconds)*time.Second, s.Now())
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, shareResponse{
		URL:       s.publicBase(r) + "/share/" + url.PathEscape(token),
		Token:     token,
		ExpiresAt: expires.UTC().Format(time.RFC3339),
	})
}

// shareDocument verifies the {token} path segment and loads the document it names.
// It writes the error answer and returns false when the link is bad, expired, or points at a deleted document.
func (s *server) shareDocument(w http.ResponseWriter, r *http.Request) (store.Document, time.Time, bool) {
	// The token is a capability. Keep it out of caches and out of the Referer header.
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")

	claims, err := s.Shares.Verify(r.PathValue("token"), s.Now())
	if err != nil {
		if errors.Is(err, auth.ErrExpiredShare) {
			writeError(w, http.StatusGone, "SHARE_EXPIRED", "this link has expired. Ask the sender for a new one")
		} else {
			writeError(w, http.StatusNotFound, "SHARE_INVALID", "this link is not valid")
		}
		return store.Document{}, time.Time{}, false
	}
	id, _ := claims.DocumentID()
	doc, err := s.Store.GetDocument(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "SHARE_INVALID", "this link is not valid")
		} else {
			s.writeStoreError(w, err)
		}
		return store.Document{}, time.Time{}, false
	}
	return doc, claims.ExpiresAt.Time, true
}

// sharedInfo describes the shared document. No sign-in is needed: the token is the credential.
func (s *server) sharedInfo(w http.ResponseWriter, r *http.Request) {
	doc, expires, ok := s.shareDocument(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, sharedDocument{
		Title:        doc.Title,
		DocumentDate: doc.DocumentDate,
		Filename:     doc.Filename,
		Mime:         doc.Mime,
		Size:         doc.Size,
		Rotation:     doc.Rotation,
		ExpiresAt:    expires.UTC().Format(time.RFC3339),
	})
}

// sharedFile streams the shared file, inline to view it, or as an attachment with ?download=1.
func (s *server) sharedFile(w http.ResponseWriter, r *http.Request) {
	doc, _, ok := s.shareDocument(w, r)
	if !ok {
		return
	}
	path, err := s.Archive.Layout.DocumentFile(doc.Folder.Path, doc.Filename)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.Log.Error("shared file missing", "id", doc.ID, "path", path, "error", err)
		writeError(w, http.StatusNotFound, "FILE_MISSING", "the file is no longer available")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", doc.Mime)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": doc.Filename}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, doc.Filename, info.ModTime(), f)
}
