package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/paperless"
	"github.com/archivr/backend/internal/store"
)

// paperlessView is what admins see. The token is masked, never returned.
type paperlessView struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	TokenSet bool   `json:"token_set"`

	InsecureTLS bool `json:"insecure_tls"`
}

func paperlessViewOf(s paperless.Settings) paperlessView {
	v := paperlessView{URL: s.URL, TokenSet: s.Token != "", InsecureTLS: s.InsecureTLS}
	if v.TokenSet {
		v.Token = maskedSecret
	}
	return v
}

func (s *server) getPaperlessSettings(w http.ResponseWriter, r *http.Request, _ store.User) {
	settings, err := paperless.LoadSettings(r.Context(), s.Store)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paperlessViewOf(settings))
}

func (s *server) putPaperlessSettings(w http.ResponseWriter, r *http.Request, _ store.User) {
	ctx := r.Context()
	var body paperless.Settings
	if !decodeBody(w, r, &body) {
		return
	}
	current, err := paperless.LoadSettings(ctx, s.Store)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	// The masked value means "unchanged". Any other value replaces the stored token.
	if body.Token == maskedSecret {
		body.Token = current.Token
	}
	next := body.Normalize()
	if err := next.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_SETTINGS", err.Error())
		return
	}
	if err := paperless.SaveSettings(ctx, s.Store, next); err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paperlessViewOf(next))
}

// testPaperless connects with the saved settings and reports how many documents the token can read.
func (s *server) testPaperless(w http.ResponseWriter, r *http.Request, _ store.User) {
	settings, err := paperless.LoadSettings(r.Context(), s.Store)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !settings.Configured() {
		writeError(w, http.StatusBadRequest, "INVALID_SETTINGS", paperless.ErrNotConfigured.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	n, err := paperless.NewClient(settings).Count(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "PAPERLESS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "documents": n})
}

func (s *server) getPaperlessImport(w http.ResponseWriter, _ *http.Request, _ store.User) {
	writeJSON(w, http.StatusOK, s.Paperless.Status())
}

func (s *server) startPaperlessImport(w http.ResponseWriter, r *http.Request, user store.User) {
	var opts paperless.Options
	if err := readJSON(w, r, &opts); err != nil && !errors.Is(err, errEmptyBody) {
		writeBodyError(w, err)
		return
	}
	if opts.FolderID != 0 && !s.folderUsable(w, r, user, opts.FolderID) {
		return
	}
	switch err := s.Paperless.Start(opts); {
	case err == nil:
		writeJSON(w, http.StatusAccepted, s.Paperless.Status())
	case errors.Is(err, paperless.ErrRunning):
		writeError(w, http.StatusConflict, "IMPORT_RUNNING", err.Error())
	case errors.Is(err, paperless.ErrNotConfigured):
		writeError(w, http.StatusBadRequest, "INVALID_SETTINGS", err.Error())
	default:
		s.writeStoreError(w, err)
	}
}

func (s *server) cancelPaperlessImport(w http.ResponseWriter, _ *http.Request, _ store.User) {
	s.Paperless.Cancel()
	writeJSON(w, http.StatusOK, s.Paperless.Status())
}

func (s *server) getPaperlessMapping(w http.ResponseWriter, r *http.Request, _ store.User) {
	m, err := paperless.LoadMapping(r.Context(), s.Store)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// putPaperlessMapping replaces the tag-to-folder rules. The order of the rules is their priority.
func (s *server) putPaperlessMapping(w http.ResponseWriter, r *http.Request, user store.User) {
	var body paperless.Mapping
	if !decodeBody(w, r, &body) {
		return
	}
	m := body.Normalize()
	if err := m.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_MAPPING", err.Error())
		return
	}
	// A folder hidden from this admin is reported like one that is gone.
	for _, rule := range m.Rules {
		ok, err := s.Store.FolderVisibleTo(r.Context(), user.ID, rule.FolderID)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if !ok {
			s.writeStoreError(w, fmt.Errorf("%w: the folder for tag %q no longer exists", archive.ErrInvalid, rule.Tag))
			return
		}
	}
	if err := m.CheckFolders(r.Context(), s.Store); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if err := paperless.SaveMapping(r.Context(), s.Store, m); err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// listPaperlessTags returns the tags on the Paperless server, to build the mapping from.
func (s *server) listPaperlessTags(w http.ResponseWriter, r *http.Request, _ store.User) {
	settings, err := paperless.LoadSettings(r.Context(), s.Store)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !settings.Configured() {
		writeError(w, http.StatusBadRequest, "INVALID_SETTINGS", paperless.ErrNotConfigured.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tags, err := paperless.NewClient(settings).Tags(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "PAPERLESS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tags})
}

// folderUsable checks that the user can see a folder before an import files into it. It writes
// the error answer and returns false when the folder is missing or hidden.
func (s *server) folderUsable(w http.ResponseWriter, r *http.Request, user store.User, id int64) bool {
	ok, err := s.Store.FolderVisibleTo(r.Context(), user.ID, id)
	if err != nil {
		s.writeStoreError(w, err)
		return false
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID", "unknown folder")
		return false
	}
	return true
}
