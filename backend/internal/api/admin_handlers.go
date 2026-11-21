package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/archivr/backend/internal/auth"
	"github.com/archivr/backend/internal/ingest"
	"github.com/archivr/backend/internal/ocr"
	"github.com/archivr/backend/internal/store"
)

const minPasswordLen = 12

// imapView is what admins see. The password is masked, never returned.
type imapView struct {
	Enabled     bool   `json:"enabled"`
	Host        string `json:"imap_host"`
	Port        int    `json:"imap_port"`
	TLS         bool   `json:"imap_tls"`
	User        string `json:"imap_user"`
	Password    string `json:"imap_password"`
	PasswordSet bool   `json:"imap_password_set"`
	Mailbox     string `json:"imap_mailbox"`
	PollSeconds int    `json:"poll_interval_seconds"`
}

func imapViewOf(s ingest.IMAPSettings) imapView {
	v := imapView{
		Enabled:     s.Enabled,
		Host:        s.Host,
		Port:        s.Port,
		TLS:         s.TLS,
		User:        s.User,
		PasswordSet: s.Password != "",
		Mailbox:     s.Mailbox,
		PollSeconds: s.PollSeconds,
	}
	if v.PasswordSet {
		v.Password = maskedSecret
	}
	return v
}

func (s *server) getIMAPSettings(w http.ResponseWriter, r *http.Request, _ store.User) {
	settings, err := ingest.LoadIMAPSettings(r.Context(), s.Store)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, imapViewOf(settings))
}

func (s *server) putIMAPSettings(w http.ResponseWriter, r *http.Request, _ store.User) {
	ctx := r.Context()
	var body ingest.IMAPSettings
	if !decodeBody(w, r, &body) {
		return
	}
	current, err := ingest.LoadIMAPSettings(ctx, s.Store)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	// The masked value means "unchanged". Any other value replaces the stored password.
	if body.Password == maskedSecret {
		body.Password = current.Password
	}
	next := body.Normalize()
	if err := next.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_SETTINGS", err.Error())
		return
	}
	if err := ingest.SaveIMAPSettings(ctx, s.Store, next); err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, imapViewOf(next))
}

// storedIMAP loads the saved settings for a connection test, and refuses an empty configuration.
func (s *server) storedIMAP(w http.ResponseWriter, r *http.Request) (ingest.IMAPSettings, bool) {
	settings, err := ingest.LoadIMAPSettings(r.Context(), s.Store)
	if err != nil {
		s.writeStoreError(w, err)
		return ingest.IMAPSettings{}, false
	}
	if settings.Host == "" {
		writeError(w, http.StatusBadRequest, "INVALID_SETTINGS", "save the mail server settings first")
		return ingest.IMAPSettings{}, false
	}
	return settings, true
}

func (s *server) testIMAP(w http.ResponseWriter, r *http.Request, _ store.User) {
	settings, ok := s.storedIMAP(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := ingest.TestIMAP(ctx, settings); err != nil {
		writeError(w, http.StatusBadGateway, "IMAP_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mailbox": settings.Mailbox})
}

func (s *server) listMailboxes(w http.ResponseWriter, r *http.Request, _ store.User) {
	settings, ok := s.storedIMAP(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	names, err := ingest.ListMailboxes(ctx, settings)
	if err != nil {
		writeError(w, http.StatusBadGateway, "IMAP_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": nonNil(names)})
}

// ocrView joins the saved OCR options with what is installed on this machine.
type ocrView struct {
	ocr.Settings
	Tesseract          bool     `json:"tesseract"`
	TesseractVersion   string   `json:"tesseract_version"`
	Poppler            bool     `json:"poppler"`
	InstalledLanguages []string `json:"installed_languages"`
	MissingLanguages   []string `json:"missing_languages"`
}

func (s *server) ocrStatusView(r *http.Request, settings ocr.Settings) ocrView {
	st := ocr.Detect(r.Context(), s.Engine)
	installed := map[string]bool{}
	for _, l := range st.InstalledLanguages {
		installed[l] = true
	}
	var missing []string
	for _, l := range strings.Split(settings.Languages, "+") {
		if st.Tesseract && !installed[l] {
			missing = append(missing, l)
		}
	}
	return ocrView{
		Settings:           settings,
		Tesseract:          st.Tesseract,
		TesseractVersion:   st.TesseractVersion,
		Poppler:            st.Poppler,
		InstalledLanguages: nonNil(st.InstalledLanguages),
		MissingLanguages:   nonNil(missing),
	}
}

func (s *server) getOCRSettings(w http.ResponseWriter, r *http.Request, _ store.User) {
	settings, err := ocr.LoadSettings(r.Context(), s.Store)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.ocrStatusView(r, settings))
}

func (s *server) putOCRSettings(w http.ResponseWriter, r *http.Request, _ store.User) {
	var body ocr.Settings
	if !decodeBody(w, r, &body) {
		return
	}
	next := body.Normalize()
	if err := next.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_SETTINGS", "languages must look like eng+fra")
		return
	}
	if err := ocr.SaveSettings(r.Context(), s.Store, next); err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.ocrStatusView(r, next))
}

func (s *server) listUsers(w http.ResponseWriter, r *http.Request, _ store.User) {
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": nonNil(users)})
}

type userCreate struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Password string `json:"password"` // optional. Without it, the account signs in with SSO only
}

func validRole(role string) bool {
	return role == "admin" || role == "user"
}

func (s *server) createUser(w http.ResponseWriter, r *http.Request, _ store.User) {
	var body userCreate
	if !decodeBody(w, r, &body) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if !strings.Contains(email, "@") || len(email) > 254 {
		writeError(w, http.StatusBadRequest, "INVALID", "enter an email address")
		return
	}
	if !validRole(body.Role) {
		writeError(w, http.StatusBadRequest, "INVALID", "role must be admin or user")
		return
	}
	hash := ""
	if body.Password != "" {
		if len(body.Password) < minPasswordLen {
			writeError(w, http.StatusBadRequest, "INVALID", "password must be at least 12 characters")
			return
		}
		h, err := auth.HashPassword(body.Password)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		hash = h
	}
	u, err := s.Store.CreateUser(r.Context(), store.User{
		Email:        email,
		Name:         strings.TrimSpace(body.Name),
		Role:         body.Role,
		PasswordHash: hash,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *server) patchUser(w http.ResponseWriter, r *http.Request, actor store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Name *string `json:"name"`
		Role *string `json:"role"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	ctx := r.Context()
	cur, err := s.Store.UserByID(ctx, id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	name := cur.Name
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
	}
	role := cur.Role
	if body.Role != nil {
		if !validRole(*body.Role) {
			writeError(w, http.StatusBadRequest, "INVALID", "role must be admin or user")
			return
		}
		role = *body.Role
	}
	if cur.Role == "admin" && role != "admin" {
		if msg := s.lastAdminProblem(ctx, cur, actor); msg != "" {
			writeError(w, http.StatusBadRequest, "INVALID", msg)
			return
		}
	}
	if err := s.Store.UpdateUserAccess(ctx, id, name, role); err != nil {
		s.writeStoreError(w, err)
		return
	}
	cur.Name, cur.Role = name, role
	writeJSON(w, http.StatusOK, cur)
}

// lastAdminProblem explains why an admin cannot be demoted or deleted, or returns "".
func (s *server) lastAdminProblem(ctx context.Context, target, actor store.User) string {
	if target.ID == actor.ID {
		return "you cannot remove your own admin role"
	}
	n, err := s.Store.CountAdmins(ctx)
	if err == nil && n <= 1 {
		return "at least one admin must remain"
	}
	return ""
}

func (s *server) deleteUser(w http.ResponseWriter, r *http.Request, actor store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	cur, err := s.Store.UserByID(ctx, id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if cur.ID == actor.ID {
		writeError(w, http.StatusBadRequest, "INVALID", "you cannot delete your own account")
		return
	}
	if cur.Role == "admin" {
		if msg := s.lastAdminProblem(ctx, cur, actor); msg != "" {
			writeError(w, http.StatusBadRequest, "INVALID", msg)
			return
		}
	}
	if err := s.Store.DeleteUser(ctx, id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) setUserPassword(w http.ResponseWriter, r *http.Request, _ store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if len(body.Password) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "INVALID", "password must be at least 12 characters")
		return
	}
	if _, err := s.Store.UserByID(r.Context(), id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if err := s.Store.SetPasswordHash(r.Context(), id, hash); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
