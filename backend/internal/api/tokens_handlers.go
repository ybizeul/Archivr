package api

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/archivr/backend/internal/auth"
	"github.com/archivr/backend/internal/store"
)

const (
	maxTokenNameLen  = 100
	maxTokenLifeDays = 3650
	maxLiveTokens    = 50 // per user, so a script cannot fill the table
)

type tokenCreate struct {
	Name          string `json:"name"`
	ExpiresInDays *int   `json:"expires_in_days"` // null or absent means the token does not expire
}

// tokenCreated carries the secret, which is shown this once and cannot be read back.
type tokenCreated struct {
	store.APIToken
	Token string `json:"token"`
}

// listTokens returns the signed-in user's own tokens. A token never authenticates this route
// or the ones below: managing tokens needs a real sign-in.
func (s *server) listTokens(w http.ResponseWriter, r *http.Request, user store.User) {
	tokens, err := s.Store.ListTokens(r.Context(), user.ID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": nonNil(tokens)})
}

func (s *server) createToken(w http.ResponseWriter, r *http.Request, user store.User) {
	var body tokenCreate
	if !decodeBody(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || utf8.RuneCountInString(name) > maxTokenNameLen {
		writeError(w, http.StatusBadRequest, "INVALID", "name must be 1 to 100 characters")
		return
	}
	now := s.Now()
	var expires *time.Time
	if body.ExpiresInDays != nil {
		if *body.ExpiresInDays < 1 || *body.ExpiresInDays > maxTokenLifeDays {
			writeError(w, http.StatusBadRequest, "INVALID", "expires_in_days must be between 1 and 3650")
			return
		}
		t := now.AddDate(0, 0, *body.ExpiresInDays)
		expires = &t
	}
	existing, err := s.Store.ListTokens(r.Context(), user.ID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if len(existing) >= maxLiveTokens {
		writeError(w, http.StatusConflict, "TOO_MANY_TOKENS", "revoke a token before creating another")
		return
	}
	token, hash, prefix, err := auth.NewToken()
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	created, err := s.Store.CreateToken(r.Context(), user.ID, name, hash, prefix, now, expires)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, tokenCreated{APIToken: created, Token: token})
}

func (s *server) deleteToken(w http.ResponseWriter, r *http.Request, user store.User) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.RevokeToken(r.Context(), user.ID, id, s.Now()); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
