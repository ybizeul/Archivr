package api

import (
	"net/http"
	"strings"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/store"
)

type folderCreate struct {
	Name     string `json:"name"`
	ParentID *int64 `json:"parent_id"` // null or absent means top level
}

func (s *server) listFolders(w http.ResponseWriter, r *http.Request, user store.User) {
	folders, err := s.Store.ListFoldersFor(r.Context(), user.ID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": nonNil(folders)})
}

func (s *server) createFolder(w http.ResponseWriter, r *http.Request, user store.User) {
	var body folderCreate
	if !decodeBody(w, r, &body) {
		return
	}
	var parent *int64
	if body.ParentID != nil && *body.ParentID != 0 {
		parent = body.ParentID
	}
	f, err := s.Archive.CreateFolder(r.Context(), parent, body.Name)
	if err == nil {
		f, err = s.Store.FolderByIDFor(r.Context(), user.ID, f.ID)
	}
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *server) patchFolder(w http.ResponseWriter, r *http.Request, user store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var p archive.FolderPatch
	if !decodeBody(w, r, &p) {
		return
	}
	f, err := s.Archive.UpdateFolder(r.Context(), id, p)
	if err == nil {
		f, err = s.Store.FolderByIDFor(r.Context(), user.ID, f.ID)
	}
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *server) deleteFolder(w http.ResponseWriter, r *http.Request, _ store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Archive.DeleteFolder(r.Context(), id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type typeBody struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (s *server) listTypes(w http.ResponseWriter, r *http.Request, user store.User) {
	types, err := s.Store.ListTypesFor(r.Context(), user.ID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": nonNil(types)})
}

func (s *server) createType(w http.ResponseWriter, r *http.Request, user store.User) {
	var body typeBody
	if !decodeBody(w, r, &body) {
		return
	}
	t, err := s.Archive.CreateType(r.Context(), body.Name, body.Color)
	if err == nil {
		t, err = s.Store.TypeByIDFor(r.Context(), user.ID, t.ID)
	}
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *server) patchType(w http.ResponseWriter, r *http.Request, user store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body typeBody
	if !decodeBody(w, r, &body) {
		return
	}
	t, err := s.Archive.UpdateType(r.Context(), id, body.Name, body.Color)
	if err == nil {
		t, err = s.Store.TypeByIDFor(r.Context(), user.ID, t.ID)
	}
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) deleteType(w http.ResponseWriter, r *http.Request, _ store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Archive.DeleteType(r.Context(), id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listCorrespondents(w http.ResponseWriter, r *http.Request, user store.User) {
	list, err := s.Store.ListCorrespondentsFor(r.Context(), user.ID, strings.TrimSpace(r.URL.Query().Get("q")))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": nonNil(list)})
}

func (s *server) listTags(w http.ResponseWriter, r *http.Request, user store.User) {
	tags, err := s.Store.ListTagsFor(r.Context(), user.ID, strings.TrimSpace(r.URL.Query().Get("q")))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": nonNil(tags)})
}

func (s *server) patchTag(w http.ResponseWriter, r *http.Request, user store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	// A tag that only private documents of other users carry is not visible: it is not found.
	if _, err := s.Store.TagByIDFor(r.Context(), user.ID, id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	t, err := s.Archive.RenameTag(r.Context(), id, body.Name)
	if err == nil {
		t, err = s.Store.TagByIDFor(r.Context(), user.ID, t.ID)
	}
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) deleteTag(w http.ResponseWriter, r *http.Request, user store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.Store.TagByIDFor(r.Context(), user.ID, id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if err := s.Archive.DeleteTag(r.Context(), id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
