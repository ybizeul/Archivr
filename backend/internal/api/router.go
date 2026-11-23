// Package api exposes the Archivr HTTP API under /api/v1.
package api

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/auth"
	"github.com/archivr/backend/internal/ingest"
	"github.com/archivr/backend/internal/ocr"
	"github.com/archivr/backend/internal/paperless"
	"github.com/archivr/backend/internal/preview"
	"github.com/archivr/backend/internal/store"
)

const (
	sessionCookieName = "archivr_session"
	deviceCookieName  = "archivr_device"
	loginPath         = "/api/v1/auth/login"
	oidcCallbackPath  = "/api/v1/auth/oidc/callback"
	// signedOutPath is where the provider sends the browser after a logout. The login page
	// reads the flag and does not start SSO again.
	signedOutPath = "/login?signed_out=1"
)

// webCSP is the content security policy of the web app. Mantine injects style elements, so
// styles need 'unsafe-inline'.
const webCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; worker-src 'self'; " +
	"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// Deps are the collaborators the handlers need. Now is injected so tests can control time.
type Deps struct {
	Store     *store.Store
	Sessions  *auth.Sessions
	Shares    *auth.Shares // share-link signer. Derived from Sessions when nil
	OIDC      *auth.OIDCService
	PublicURL string // external base URL, e.g. https://archive.example.org. Derived from the request when empty
	// OIDCLogoutURL is the provider's logout endpoint. When set, it is used instead of the discovery document's end_session_endpoint.
	OIDCLogoutURL string
	TrustProxy    bool // trust X-Forwarded-For/-Host/-Proto for the client IP and public URL. Enable only behind a proxy you control
	// ProxyHops is how many trusted proxies append to X-Forwarded-For. The client IP is the entry the outermost one added. 0 means 1
	ProxyHops int
	Now       func() time.Time

	Archive   *archive.Service
	Ingest    *ingest.Importer
	Paperless *paperless.Runner
	OCR       *ocr.Pool
	Engine    ocr.Engine
	Previews  *preview.Cache
	WatchDir  string
	Web       http.Handler // the embedded single-page app; nil serves 404 outside /api
	Log       *slog.Logger
}

type server struct {
	Deps
	devices       *auth.Devices
	limits        loginLimits
	passwordSlots chan struct{}
}

// NewRouter returns the handler for /healthz, /api/v1/ and the web app.
func NewRouter(d Deps) http.Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Shares == nil && d.Sessions != nil {
		d.Shares = d.Sessions.Shares()
	}
	s := &server{
		Deps:          d,
		limits:        newLoginLimits(),
		passwordSlots: make(chan struct{}, maxConcurrentPasswordChecks),
	}
	if d.Sessions != nil {
		s.devices = d.Sessions.Devices()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Sign-in and session.
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/auth/me", s.authed(false, s.me))
	mux.HandleFunc("GET /api/v1/auth/oidc/status", s.oidcStatus)
	mux.HandleFunc("GET /api/v1/auth/oidc/start", s.oidcStart)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", s.oidcCallback)

	// Inbox and documents.
	mux.HandleFunc("GET /api/v1/inbox/count", s.authed(false, s.inboxCount))
	mux.HandleFunc("GET /api/v1/documents", s.authed(false, s.listDocuments))
	mux.HandleFunc("POST /api/v1/documents", s.authed(false, s.uploadDocument))
	mux.HandleFunc("GET /api/v1/documents/{id}", s.authed(false, s.getDocument))
	mux.HandleFunc("PATCH /api/v1/documents/{id}", s.authed(false, s.patchDocument))
	mux.HandleFunc("DELETE /api/v1/documents/{id}", s.authed(false, s.deleteDocument))
	mux.HandleFunc("PUT /api/v1/documents/{id}/favorite", s.authed(false, s.addFavorite))
	mux.HandleFunc("DELETE /api/v1/documents/{id}/favorite", s.authed(false, s.removeFavorite))
	mux.HandleFunc("POST /api/v1/trash/empty", s.authed(true, s.emptyTrash))
	mux.HandleFunc("GET /api/v1/documents/{id}/file", s.authed(false, s.documentFile))
	mux.HandleFunc("PUT /api/v1/documents/{id}/file", s.authed(false, s.replaceDocumentFile))
	mux.HandleFunc("GET /api/v1/documents/{id}/thumbnail", s.authed(false, s.documentThumbnail))
	mux.HandleFunc("GET /api/v1/documents/{id}/suggestions", s.authed(false, s.documentSuggestions))
	mux.HandleFunc("POST /api/v1/documents/{id}/reocr", s.authed(false, s.reOCR))

	// Share links. Creating one needs a session. Opening one needs no sign-in: the signed
	// token in the path is the credential, and it grants read access to one document.
	mux.HandleFunc("POST /api/v1/documents/{id}/share", s.authed(false, s.createShare))
	mux.HandleFunc("GET /api/v1/shares/{token}", s.sharedInfo)
	mux.HandleFunc("GET /api/v1/shares/{token}/file", s.sharedFile)

	// Folders, types, correspondents and tags.
	mux.HandleFunc("GET /api/v1/folders", s.authed(false, s.listFolders))
	mux.HandleFunc("POST /api/v1/folders", s.authed(false, s.createFolder))
	mux.HandleFunc("PATCH /api/v1/folders/{id}", s.authed(false, s.patchFolder))
	mux.HandleFunc("DELETE /api/v1/folders/{id}", s.authed(false, s.deleteFolder))
	mux.HandleFunc("GET /api/v1/types", s.authed(false, s.listTypes))
	mux.HandleFunc("POST /api/v1/types", s.authed(true, s.createType))
	mux.HandleFunc("PATCH /api/v1/types/{id}", s.authed(true, s.patchType))
	mux.HandleFunc("DELETE /api/v1/types/{id}", s.authed(true, s.deleteType))
	mux.HandleFunc("GET /api/v1/correspondents", s.authed(false, s.listCorrespondents))
	mux.HandleFunc("GET /api/v1/tags", s.authed(false, s.listTags))
	mux.HandleFunc("PATCH /api/v1/tags/{id}", s.authed(false, s.patchTag))
	mux.HandleFunc("DELETE /api/v1/tags/{id}", s.authed(false, s.deleteTag))

	// Admin: sign-in, mail, OCR, Paperless import, users.
	mux.HandleFunc("GET /api/v1/admin/settings/auth", s.authed(true, s.getAuthSettings))
	mux.HandleFunc("PUT /api/v1/admin/settings/auth", s.authed(true, s.putAuthSettings))
	mux.HandleFunc("GET /api/v1/admin/settings/imap", s.authed(true, s.getIMAPSettings))
	mux.HandleFunc("PUT /api/v1/admin/settings/imap", s.authed(true, s.putIMAPSettings))
	mux.HandleFunc("POST /api/v1/admin/settings/imap/test", s.authed(true, s.testIMAP))
	mux.HandleFunc("GET /api/v1/admin/settings/imap/mailboxes", s.authed(true, s.listMailboxes))
	mux.HandleFunc("GET /api/v1/admin/settings/ocr", s.authed(true, s.getOCRSettings))
	mux.HandleFunc("PUT /api/v1/admin/settings/ocr", s.authed(true, s.putOCRSettings))
	mux.HandleFunc("POST /api/v1/admin/ocr/run-missing", s.authed(true, s.runMissingOCR))
	mux.HandleFunc("GET /api/v1/admin/settings/paperless", s.authed(true, s.getPaperlessSettings))
	mux.HandleFunc("PUT /api/v1/admin/settings/paperless", s.authed(true, s.putPaperlessSettings))
	mux.HandleFunc("POST /api/v1/admin/settings/paperless/test", s.authed(true, s.testPaperless))
	mux.HandleFunc("GET /api/v1/admin/paperless/tags", s.authed(true, s.listPaperlessTags))
	mux.HandleFunc("GET /api/v1/admin/paperless/mapping", s.authed(true, s.getPaperlessMapping))
	mux.HandleFunc("PUT /api/v1/admin/paperless/mapping", s.authed(true, s.putPaperlessMapping))
	mux.HandleFunc("GET /api/v1/admin/paperless/import", s.authed(true, s.getPaperlessImport))
	mux.HandleFunc("POST /api/v1/admin/paperless/import", s.authed(true, s.startPaperlessImport))
	mux.HandleFunc("POST /api/v1/admin/paperless/import/cancel", s.authed(true, s.cancelPaperlessImport))
	mux.HandleFunc("GET /api/v1/admin/users", s.authed(true, s.listUsers))
	mux.HandleFunc("POST /api/v1/admin/users", s.authed(true, s.createUser))
	mux.HandleFunc("PATCH /api/v1/admin/users/{id}", s.authed(true, s.patchUser))
	mux.HandleFunc("DELETE /api/v1/admin/users/{id}", s.authed(true, s.deleteUser))
	mux.HandleFunc("POST /api/v1/admin/users/{id}/password", s.authed(true, s.setUserPassword))

	// Unknown API paths get a JSON 404, not the web app.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such endpoint")
	})
	if d.Web != nil {
		mux.Handle("/", d.Web)
	}

	// Browsers mark cross-origin requests. Refusing state-changing ones means a page on another
	// origin, a sibling subdomain included, cannot act with the user's cookie.
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusForbidden, "CROSS_ORIGIN", "cross-origin requests are refused")
	}))
	return s.securityHeaders(cop.Handler(mux))
}

// securityHeaders sets the headers every response carries. Pages of the web app also get the
// content security policy and are kept out of frames. API responses are JSON or stored files,
// not pages that run the app, so they do not.
func (s *server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if s.servesHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Content-Security-Policy", webCSP)
			h.Set("X-Frame-Options", "DENY")
		}
		next.ServeHTTP(w, r)
	})
}

// servesHTTPS reports whether browsers reach Archivr over HTTPS: PUBLIC_URL says so, or the
// request came over TLS, directly or through a trusted proxy.
func (s *server) servesHTTPS(r *http.Request) bool {
	if s.PublicURL != "" {
		return strings.HasPrefix(s.PublicURL, "https://")
	}
	return r.TLS != nil || (s.TrustProxy && strings.EqualFold(firstForwardedValue(r.Header.Get("X-Forwarded-Proto")), "https"))
}

// authed wraps a handler that needs a signed-in user, and optionally an admin.
func (s *server) authed(admin bool, next func(http.ResponseWriter, *http.Request, store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.currentUser(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "sign in required")
			return
		}
		if admin && user.Role != "admin" {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "admin role required")
			return
		}
		// Archive mutations act for this user: they cannot reach what a private folder hides.
		next(w, r.WithContext(archive.WithActor(r.Context(), user.ID)), user)
	}
}

// currentUser reads the session from the cookie, or from a Bearer token for API clients.
// The user is reloaded from the database on every request, so a removed account or a
// changed role takes effect at once.
func (s *server) currentUser(r *http.Request) (store.User, bool) {
	token := ""
	if c, err := r.Cookie(sessionCookieName); err == nil {
		token = c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token = strings.TrimPrefix(h, "Bearer ")
	}
	if token == "" {
		return store.User{}, false
	}
	claims, err := s.Sessions.Verify(token, s.Now())
	if err != nil {
		return store.User{}, false
	}
	id, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil {
		return store.User{}, false
	}
	user, err := s.Store.UserByID(r.Context(), id)
	if err != nil {
		return store.User{}, false
	}
	return user, true
}

// isHTTPS reports whether the client reached us over TLS, directly or via a proxy.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *server) publicBase(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/")
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if s.TrustProxy {
		// Behind a reverse proxy, the browser-facing host is in X-Forwarded-Host.
		// Take the first entry when several proxies have appended to it.
		if fh := firstForwardedValue(r.Header.Get("X-Forwarded-Host")); fh != "" {
			host = fh
		}
		if fp := firstForwardedValue(r.Header.Get("X-Forwarded-Proto")); fp != "" {
			if strings.EqualFold(fp, "https") {
				scheme = "https"
			} else if strings.EqualFold(fp, "http") {
				scheme = "http"
			}
		}
	}
	// The default HTTPS port is implied, so drop it from the URL.
	if scheme == "https" {
		host = strings.TrimSuffix(host, ":443")
	}
	return scheme + "://" + host
}

// firstForwardedValue returns the first entry of a comma-separated forwarded header.
func firstForwardedValue(v string) string {
	if i := strings.Index(v, ","); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// clientIP returns the address the sign-in limits count against. Behind trusted proxies it is
// the X-Forwarded-For entry the outermost one added: entries further left are whatever the
// client sent. An IPv6 address is reduced to its /64, the block one subscriber usually holds.
func (s *server) clientIP(r *http.Request) string {
	addr := ""
	if s.TrustProxy {
		var hops []string
		for _, v := range r.Header.Values("X-Forwarded-For") {
			for _, h := range strings.Split(v, ",") {
				if h = strings.TrimSpace(h); h != "" {
					hops = append(hops, h)
				}
			}
		}
		if len(hops) > 0 {
			addr = hops[max(len(hops)-max(s.ProxyHops, 1), 0)]
		}
	}
	if addr == "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		addr = host
	}
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return addr
	}
	ip = ip.Unmap()
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return ip.String()
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.SessionTTL.Seconds()),
	})
}

// setDeviceCookie marks this browser as one that has signed in to the account before. Only the
// login endpoint reads it.
func setDeviceCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     deviceCookieName,
		Value:    token,
		Path:     loginPath,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(auth.DeviceTTL.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
