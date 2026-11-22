package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/auth"
	"github.com/archivr/backend/internal/db"
	"github.com/archivr/backend/internal/ingest"
	"github.com/archivr/backend/internal/ocr"
	"github.com/archivr/backend/internal/paperless"
	"github.com/archivr/backend/internal/preview"
	"github.com/archivr/backend/internal/storage"
	"github.com/archivr/backend/internal/store"
)

const (
	adminEmail  = "admin@example.org"
	readerEmail = "reader@example.org"
	password    = "correct horse battery"
)

type fixture struct {
	t        *testing.T
	h        http.Handler
	store    *store.Store
	sessions *auth.Sessions
	skew     time.Duration // added to the server clock, so tests can move time forward
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	st := store.New(conn)

	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []store.User{
		{Email: adminEmail, Name: "Admin", Role: "admin", PasswordHash: hash},
		{Email: readerEmail, Name: "Reader", Role: "user", PasswordHash: hash},
	} {
		if _, err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	sessions, err := auth.NewSessions("test-secret")
	if err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	layout := storage.Layout{DataDir: dataDir}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	arch := &archive.Service{Store: st, Layout: layout, Log: quiet}
	pool := ocr.NewPool(st, layout, ocr.Engine{}, 1, quiet)
	arch.OCR = pool

	importer := &ingest.Importer{Archive: arch, Store: st}

	f := &fixture{t: t, store: st, sessions: sessions}
	h := NewRouter(Deps{
		Store:     st,
		Sessions:  sessions,
		Now:       func() time.Time { return time.Now().Add(f.skew) },
		OIDC:      auth.NewOIDCService(),
		Limiter:   auth.NewLoginLimiter(15*time.Minute, 15*time.Minute, 8),
		PublicURL: "https://archive.test",
		Archive:   arch,
		Ingest:    importer,
		Paperless: &paperless.Runner{Importer: importer, Archive: arch, Store: st, Log: quiet},
		OCR:       pool,
		Previews:  &preview.Cache{Dir: filepath.Join(dataDir, "cache", "thumbs")},
		WatchDir:  filepath.Join(dataDir, "ingest"),
		Log:       quiet,
	})
	f.h = h
	return f
}

func (f *fixture) do(method, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	f.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) login(email, pw string) *httptest.ResponseRecorder {
	return f.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": pw})
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	return nil
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}

func TestLoginSetsHttpOnlySessionCookie(t *testing.T) {
	f := newFixture(t)
	rec := f.login(adminEmail, password)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	c := sessionCookie(rec)
	if c == nil {
		t.Fatal("no session cookie set")
	}
	if !c.HttpOnly {
		t.Fatal("session cookie is not HttpOnly")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite = %v, want Lax", c.SameSite)
	}
}

func TestLoginEmailIsCaseInsensitive(t *testing.T) {
	f := newFixture(t)
	if rec := f.login("ADMIN@Example.org", password); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestWrongPasswordIsUnauthorized(t *testing.T) {
	f := newFixture(t)
	rec := f.login(adminEmail, "wrong")
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "INVALID_CREDENTIALS" {
		t.Fatalf("status = %d code = %s", rec.Code, errorCode(t, rec))
	}
	if sessionCookie(rec) != nil {
		t.Fatal("cookie set on failed login")
	}
}

func TestUnknownEmailLooksLikeWrongPassword(t *testing.T) {
	f := newFixture(t)
	rec := f.login("nobody@example.org", password)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "INVALID_CREDENTIALS" {
		t.Fatalf("status = %d code = %s", rec.Code, errorCode(t, rec))
	}
}

func TestMeRequiresSession(t *testing.T) {
	f := newFixture(t)
	if rec := f.do(http.MethodGet, "/api/v1/auth/me", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous /me status = %d", rec.Code)
	}

	c := sessionCookie(f.login(readerEmail, password))
	rec := f.do(http.MethodGet, "/api/v1/auth/me", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("/me status = %d", rec.Code)
	}
	var u store.User
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if u.Email != readerEmail || u.PasswordHash != "" {
		t.Fatalf("/me returned %+v; password hash must not be exposed", u)
	}
}

func TestBearerTokenAuthenticates(t *testing.T) {
	f := newFixture(t)
	rec := f.login(readerEmail, password)
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Token == "" {
		t.Fatalf("no token in login body: %s", rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+body.Token)
	out := httptest.NewRecorder()
	f.h.ServeHTTP(out, req)
	if out.Code != http.StatusOK {
		t.Fatalf("bearer /me status = %d", out.Code)
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	f := newFixture(t)
	c := sessionCookie(f.login(readerEmail, password))
	rec := f.do(http.MethodPost, "/api/v1/auth/logout", nil, c)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	cleared := sessionCookie(rec)
	if cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Fatalf("cookie not cleared: %+v", cleared)
	}
}

// fakeIdentityProvider serves OIDC discovery. endSession is the path of its logout endpoint, or "" for none.
func fakeIdentityProvider(t *testing.T, endSession string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		doc := map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
			"jwks_uri":               srv.URL + "/keys",
		}
		if endSession != "" {
			doc["end_session_endpoint"] = srv.URL + endSession
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLogoutReturnsProviderEndSessionURL(t *testing.T) {
	idp := fakeIdentityProvider(t, "/logout")
	f := newFixture(t)
	admin := sessionCookie(f.login(adminEmail, password))
	f.do(http.MethodPut, "/api/v1/admin/settings/auth", map[string]any{
		"oidc_enabled":   true,
		"oidc_issuer":    idp.URL,
		"oidc_client_id": "archivr",
	}, admin)

	c := sessionCookie(f.login(readerEmail, password))
	rec := f.do(http.MethodPost, "/api/v1/auth/logout", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Redirect string `json:"redirect"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad body %s: %v", rec.Body.String(), err)
	}
	u, err := url.Parse(body.Redirect)
	if err != nil || u.Scheme+"://"+u.Host+u.Path != idp.URL+"/logout" {
		t.Fatalf("redirect = %q, want provider logout endpoint", body.Redirect)
	}
	if u.Query().Get("client_id") != "archivr" {
		t.Fatalf("client_id missing from %q", body.Redirect)
	}
	if !strings.HasSuffix(u.Query().Get("post_logout_redirect_uri"), "/login?signed_out=1") {
		t.Fatalf("post_logout_redirect_uri = %q", u.Query().Get("post_logout_redirect_uri"))
	}
	if cleared := sessionCookie(rec); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %+v", cleared)
	}
}

func TestLogoutWithoutProviderEndSessionStillClearsCookie(t *testing.T) {
	idp := fakeIdentityProvider(t, "")
	f := newFixture(t)
	admin := sessionCookie(f.login(adminEmail, password))
	f.do(http.MethodPut, "/api/v1/admin/settings/auth", map[string]any{
		"oidc_enabled":   true,
		"oidc_issuer":    idp.URL,
		"oidc_client_id": "archivr",
	}, admin)

	c := sessionCookie(f.login(readerEmail, password))
	rec := f.do(http.MethodPost, "/api/v1/auth/logout", nil, c)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if cleared := sessionCookie(rec); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %+v", cleared)
	}
}

func TestRemovedUserLosesAccess(t *testing.T) {
	f := newFixture(t)
	c := sessionCookie(f.login(readerEmail, password))
	u, _ := f.store.UserByEmail(context.Background(), readerEmail)
	if _, err := f.store.DB.Exec(`DELETE FROM users WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(http.MethodGet, "/api/v1/auth/me", nil, c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user still signed in, status = %d", rec.Code)
	}
}

func TestForgedSessionRejected(t *testing.T) {
	f := newFixture(t)
	other, _ := auth.NewSessions("attacker-key")
	token, _ := other.Issue(1, "Admin", time.Now())
	c := &http.Cookie{Name: sessionCookieName, Value: token}
	if rec := f.do(http.MethodGet, "/api/v1/auth/me", nil, c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged session accepted, status = %d", rec.Code)
	}
}

func TestLoginRateLimitBlocksAfterEightFailures(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 8; i++ {
		if rec := f.login(readerEmail, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d", i+1, rec.Code)
		}
	}
	rec := f.login(readerEmail, password)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "RATE_LIMITED" {
		t.Fatalf("correct password during block: status = %d", rec.Code)
	}
}

func TestAdminSettingsRequireAdmin(t *testing.T) {
	f := newFixture(t)
	c := sessionCookie(f.login(readerEmail, password))
	if rec := f.do(http.MethodGet, "/api/v1/admin/settings/auth", nil, c); rec.Code != http.StatusForbidden {
		t.Fatalf("reader GET status = %d", rec.Code)
	}
	if rec := f.do(http.MethodPut, "/api/v1/admin/settings/auth", map[string]any{}, c); rec.Code != http.StatusForbidden {
		t.Fatalf("reader PUT status = %d", rec.Code)
	}
}

func TestAdminSettingsMaskSecretAndKeepItOnMaskedSave(t *testing.T) {
	f := newFixture(t)
	c := sessionCookie(f.login(adminEmail, password))

	rec := f.do(http.MethodPut, "/api/v1/admin/settings/auth", map[string]any{
		"oidc_enabled":       true,
		"oidc_issuer":        "https://auth.example.org/",
		"oidc_client_id":     "archivr",
		"oidc_client_secret": "s3cret",
		"oidc_admin_group":   "archivr-admins",
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body %s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("s3cret")) {
		t.Fatal("client secret leaked in response")
	}
	var view map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	if view["oidc_client_secret"] != "********" || view["oidc_secret_set"] != true {
		t.Fatalf("secret not masked: %v", view)
	}
	if view["oidc_issuer"] != "https://auth.example.org" {
		t.Fatalf("issuer not normalised: %v", view["oidc_issuer"])
	}

	// Saving the masked value again must keep the stored secret.
	rec = f.do(http.MethodPut, "/api/v1/admin/settings/auth", map[string]any{
		"oidc_enabled":       true,
		"oidc_issuer":        "https://auth.example.org",
		"oidc_client_id":     "archivr-2",
		"oidc_client_secret": "********",
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("second PUT status = %d", rec.Code)
	}
	stored, err := auth.LoadSettings(context.Background(), f.store)
	if err != nil {
		t.Fatal(err)
	}
	if stored.OIDCClientSecret != "s3cret" || stored.OIDCClientID != "archivr-2" {
		t.Fatalf("stored settings = %+v", stored)
	}
}

func TestOIDCOnlyBlocksLocalLogin(t *testing.T) {
	f := newFixture(t)
	c := sessionCookie(f.login(adminEmail, password))
	rec := f.do(http.MethodPut, "/api/v1/admin/settings/auth", map[string]any{
		"oidc_enabled":        true,
		"disable_local_login": true,
		"oidc_issuer":         "https://auth.example.org",
		"oidc_client_id":      "archivr",
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d", rec.Code)
	}

	status := f.do(http.MethodGet, "/api/v1/auth/oidc/status", nil)
	var s map[string]bool
	_ = json.Unmarshal(status.Body.Bytes(), &s)
	if !s["enabled"] || !s["disable_local_login"] {
		t.Fatalf("status = %v", s)
	}

	rec = f.login(readerEmail, password)
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "LOCAL_LOGIN_DISABLED" {
		t.Fatalf("local login during oidc-only: status = %d", rec.Code)
	}
}

func TestDisableLocalLoginRequiresOIDC(t *testing.T) {
	f := newFixture(t)
	c := sessionCookie(f.login(adminEmail, password))
	rec := f.do(http.MethodPut, "/api/v1/admin/settings/auth", map[string]any{
		"oidc_enabled":        true,
		"disable_local_login": true,
	}, c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for incomplete oidc config", rec.Code)
	}
}

func TestOIDCStatusDisabledByDefault(t *testing.T) {
	f := newFixture(t)
	rec := f.do(http.MethodGet, "/api/v1/auth/oidc/status", nil)
	var s map[string]bool
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	if s["enabled"] || s["disable_local_login"] {
		t.Fatalf("status = %v", s)
	}
}

func TestOIDCCallbackWithoutStateRedirectsToLoginWithCode(t *testing.T) {
	f := newFixture(t)
	rec := f.do(http.MethodGet, "/api/v1/auth/oidc/callback?code=x", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?oidc_error=OIDC_CALLBACK_INVALID" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestOIDCCallbackUnknownStateIsRejected(t *testing.T) {
	f := newFixture(t)
	c := sessionCookie(f.login(adminEmail, password))
	// Enable OIDC so the callback reaches the state check. No provider is contacted,
	// because the state is looked up before discovery.
	f.do(http.MethodPut, "/api/v1/admin/settings/auth", map[string]any{
		"oidc_enabled":   true,
		"oidc_issuer":    "https://auth.example.org",
		"oidc_client_id": "archivr",
	}, c)

	rec := f.do(http.MethodGet, "/api/v1/auth/oidc/callback?state=forged&code=x", nil)
	if loc := rec.Header().Get("Location"); loc != "/login?oidc_error=OIDC_STATE_INVALID" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestOIDCStartWhenDisabledIsRejected(t *testing.T) {
	f := newFixture(t)
	rec := f.do(http.MethodGet, "/api/v1/auth/oidc/start?redirect=/", nil)
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "OIDC_DISABLED" {
		t.Fatalf("status = %d", rec.Code)
	}
}
