package api

import (
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/archivr/backend/internal/auth"
	"github.com/archivr/backend/internal/store"
)

// dummyHash is verified against when the email is unknown, so an unknown account
// takes as long to reject as a wrong password does.
var (
	dummyOnce sync.Once
	dummyHash string
)

func unknownUserHash() string {
	dummyOnce.Do(func() {
		h, err := auth.HashPassword("archivr-timing-equaliser")
		if err == nil {
			dummyHash = h
		}
	})
	return dummyHash
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body loginRequest
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	now := s.Now()

	settings, err := auth.LoadSettings(ctx, s.Store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", "could not read sign-in settings")
		return
	}
	if settings.OIDCReady() && settings.DisableLocalLogin {
		writeError(w, http.StatusForbidden, "LOCAL_LOGIN_DISABLED", "local sign-in is turned off. Use SSO")
		return
	}

	key := email + "|" + s.clientIP(r)
	if !s.Limiter.Allowed(key, now) {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many sign-in attempts. Wait 15 minutes and try again")
		return
	}

	user, err := s.Store.UserByEmail(ctx, email)
	hash := user.PasswordHash
	if errors.Is(err, store.ErrNotFound) {
		hash = unknownUserHash()
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", "could not read account")
		return
	}

	if err != nil || !auth.VerifyPassword(body.Password, hash) {
		s.Limiter.Failure(key, now)
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "email or password is wrong")
		return
	}

	s.Limiter.Success(key)
	token, err := s.issueSession(w, r, user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_ERROR", "could not start session")
		return
	}
	// The token is in the body for API clients. The browser app relies on the cookie alone.
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "token": token})
}

// issueSession signs a session for user and sets it as the cookie.
func (s *server) issueSession(w http.ResponseWriter, r *http.Request, user store.User) (string, error) {
	token, err := s.Sessions.Issue(user.ID, user.Name, s.Now())
	if err != nil {
		return "", err
	}
	setSessionCookie(w, r, token)
	return token, nil
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) me(w http.ResponseWriter, r *http.Request, user store.User) {
	writeJSON(w, http.StatusOK, user)
}

func (s *server) oidcStatus(w http.ResponseWriter, r *http.Request) {
	settings, err := auth.LoadSettings(r.Context(), s.Store)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": false, "disable_local_login": false})
		return
	}
	ready := settings.OIDCReady()
	writeJSON(w, http.StatusOK, map[string]bool{
		"enabled":             ready,
		"disable_local_login": ready && settings.DisableLocalLogin,
	})
}

func (s *server) oidcStart(w http.ResponseWriter, r *http.Request) {
	settings, err := auth.LoadSettings(r.Context(), s.Store)
	if err != nil || !settings.OIDCReady() {
		writeError(w, http.StatusBadRequest, "OIDC_DISABLED", "sso is not configured")
		return
	}
	redirectTo := auth.SanitizeRedirect(r.URL.Query().Get("redirect"))
	target, err := s.OIDC.AuthURL(r.Context(), settings, s.publicBase(r)+oidcCallbackPath, redirectTo, s.Now())
	if err != nil {
		writeError(w, http.StatusBadGateway, "OIDC_DISCOVERY_FAILED", "could not reach the sign-in provider")
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	state, code := q.Get("state"), q.Get("code")
	if state == "" || code == "" {
		s.loginWithError(w, r, "OIDC_CALLBACK_INVALID")
		return
	}

	settings, err := auth.LoadSettings(ctx, s.Store)
	if err != nil || !settings.OIDCReady() {
		s.loginWithError(w, r, "OIDC_DISABLED")
		return
	}

	identity, redirectTo, err := s.OIDC.Complete(ctx, settings, s.publicBase(r)+oidcCallbackPath, state, code, s.Now())
	if err != nil {
		s.loginWithError(w, r, oidcErrorCode(err))
		return
	}

	role := auth.RoleForGroups(identity.Groups, settings.OIDCAdminGroup)
	user, err := s.Store.UserByEmail(ctx, identity.Email)
	switch {
	case err == nil:
		if err := s.Store.UpdateUserAccess(ctx, user.ID, identity.Name, role); err != nil {
			s.loginWithError(w, r, "OIDC_USER_ERROR")
			return
		}
		user.Name, user.Role = identity.Name, role
	case errors.Is(err, store.ErrNotFound):
		user, err = s.Store.CreateUser(ctx, store.User{
			Email: identity.Email,
			Name:  identity.Name,
			Role:  role,
		})
		if err != nil {
			s.loginWithError(w, r, "OIDC_USER_ERROR")
			return
		}
	default:
		s.loginWithError(w, r, "OIDC_USER_ERROR")
		return
	}

	if _, err := s.issueSession(w, r, user); err != nil {
		s.loginWithError(w, r, "OIDC_USER_ERROR")
		return
	}
	// The cookie is set on this redirect. The target was already checked by SanitizeRedirect.
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

// loginWithError sends the browser back to the login page with a stable error code.
func (s *server) loginWithError(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/login?oidc_error="+code, http.StatusFound)
}

func oidcErrorCode(err error) string {
	switch {
	case errors.Is(err, auth.ErrOIDCState):
		return "OIDC_STATE_INVALID"
	case errors.Is(err, auth.ErrOIDCNonce):
		return "OIDC_NONCE_MISMATCH"
	case errors.Is(err, auth.ErrOIDCNoEmail):
		return "OIDC_EMAIL_REQUIRED"
	case errors.Is(err, auth.ErrOIDCEmailUnverified):
		return "OIDC_EMAIL_UNVERIFIED"
	case errors.Is(err, auth.ErrOIDCNotConfigured):
		return "OIDC_DISABLED"
	default:
		return "OIDC_FAILED"
	}
}
