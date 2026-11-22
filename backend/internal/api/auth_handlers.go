package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/archivr/backend/internal/auth"
	"github.com/archivr/backend/internal/store"
)

// Sign-in limits. Each counts attempts over limitWindow and refuses more for limitWindow once reached.
const (
	maxAttemptsPerClient  = 8  // one email from one address, or from one browser the account recognises
	maxAttemptsPerAccount = 20 // one email from browsers it does not recognise, whatever the address
	maxAttemptsPerAddress = 30 // any email from one address
	limitWindow           = 15 * time.Minute

	// maxConcurrentPasswordChecks bounds memory: each argon2id check holds 64 MiB.
	maxConcurrentPasswordChecks = 2
	// passwordQueueWait is how long a sign-in waits for a free check before it is refused.
	passwordQueueWait = 5 * time.Second
)

// loginLimits are the caps a password sign-in must pass. A browser that has signed in to the
// account before is counted on its own and not against the account, so failures from elsewhere
// cannot lock its owner out.
type loginLimits struct {
	client  *auth.LoginLimiter // email and address, or recognised browser. A success clears it
	account *auth.LoginLimiter // email, for browsers the account does not recognise. A success clears it
	address *auth.LoginLimiter // address alone. A success does not clear it
}

func newLoginLimits() loginLimits {
	return loginLimits{
		client:  auth.NewLoginLimiter(limitWindow, limitWindow, maxAttemptsPerClient),
		account: auth.NewLoginLimiter(limitWindow, limitWindow, maxAttemptsPerAccount),
		address: auth.NewLoginLimiter(limitWindow, limitWindow, maxAttemptsPerAddress),
	}
}

// loginKeys are the limiter keys of one sign-in. account is empty for a recognised browser.
type loginKeys struct {
	address, client, account string
}

func (s *server) loginKeys(r *http.Request, email string, user store.User) loginKeys {
	ip := s.clientIP(r)
	keys := loginKeys{address: ip, client: email + "|" + ip, account: email}
	if c, err := r.Cookie(deviceCookieName); err == nil && user.ID != 0 && s.devices != nil {
		if id, ok := s.devices.Verify(c.Value, user.ID, s.Now()); ok {
			keys.client, keys.account = "device|"+id, ""
		}
	}
	return keys
}

// allowLogin counts the attempt against each key and reports whether all of them allow it.
func (s *server) allowLogin(k loginKeys, now time.Time) bool {
	if !s.limits.address.Attempt(k.address, now) || !s.limits.client.Attempt(k.client, now) {
		return false
	}
	return k.account == "" || s.limits.account.Attempt(k.account, now)
}

// checkPassword verifies password against hash. An empty hash, for an unknown email or an
// account without a local password, is never a match, but a dummy hash is still checked so that
// every refusal takes as long as a wrong password. busy is true when no check slot became free.
func (s *server) checkPassword(ctx context.Context, password, hash string) (ok, busy bool) {
	timer := time.NewTimer(passwordQueueWait)
	defer timer.Stop()
	select {
	case s.passwordSlots <- struct{}{}:
		defer func() { <-s.passwordSlots }()
	case <-timer.C:
		return false, true
	case <-ctx.Done():
		return false, true
	}
	if hash == "" {
		auth.VerifyPassword(password, unknownUserHash())
		return false, false
	}
	return auth.VerifyPassword(password, hash), false
}

// dummyHash is verified against when there is no password to check, so an unknown account
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
	if !decodeBody(w, r, &body) {
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

	user, err := s.Store.UserByEmail(ctx, email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "DB_ERROR", "could not read account")
		return
	}
	keys := s.loginKeys(r, email, user)
	if !s.allowLogin(keys, now) {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many sign-in attempts. Wait 15 minutes and try again")
		return
	}
	ok, busy := s.checkPassword(ctx, body.Password, user.PasswordHash)
	if busy {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "BUSY", "too many sign-ins at once. Try again in a moment")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "email or password is wrong")
		return
	}

	s.limits.client.Success(keys.client)
	if keys.account != "" {
		s.limits.account.Success(keys.account)
	}
	token, err := s.issueSession(w, r, user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_ERROR", "could not start session")
		return
	}
	if device, err := s.devices.Issue(user.ID, now); err == nil {
		setDeviceCookie(w, r, device)
	} else {
		s.Log.Warn("could not issue device cookie", "error", err)
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

// logout clears the session cookie. With SSO on, it also returns the provider's logout URL so the
// browser can end the provider session too. If the provider has none, or cannot be reached,
// the local sign-out still happens.
func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w, r)
	if settings, err := auth.LoadSettings(r.Context(), s.Store); err == nil && settings.OIDCReady() {
		target, err := s.OIDC.EndSessionURL(r.Context(), settings, s.OIDCLogoutURL, s.publicBase(r)+signedOutPath)
		if err == nil && target != "" {
			writeJSON(w, http.StatusOK, map[string]string{"redirect": target})
			return
		}
	}
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
