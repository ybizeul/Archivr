package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/archivr/backend/internal/store"
)

// loginFrom signs in through a trusted proxy that reports xff in X-Forwarded-For.
func loginFrom(f *fixture, xff, email, pw string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, loginPath, strings.NewReader(fmt.Sprintf(`{"email":%q,"password":%q}`, email, pw)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", xff)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func deviceCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == deviceCookieName {
			return c
		}
	}
	return nil
}

// An unknown email, or an account without a local password, is checked against a dummy hash,
// so its refusal takes as long as a wrong password and does not reveal which emails exist.
func TestRefusalTimeDoesNotRevealAccounts(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.CreateUser(context.Background(), store.User{Email: "sso@example.org", Name: "SSO", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	f.login("warmup@example.org", "wrong") // computes the dummy hash once
	fastest := func(email string) time.Duration {
		best := time.Hour
		for i := 0; i < 3; i++ {
			start := time.Now()
			if rec := f.login(email, "wrong"); rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s: status %d", email, rec.Code)
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	wrong := fastest(readerEmail)
	for _, email := range []string{"nobody@example.org", "sso@example.org"} {
		if d := fastest(email); d < wrong/4 {
			t.Errorf("%s refused in %v, a wrong password takes %v", email, d, wrong)
		}
	}
}

// Attempts count when they start, so a burst cannot get more password checks than the limit allows.
func TestConcurrentGuessesStopAtTheLimit(t *testing.T) {
	f := newFixture(t)
	const n = 16
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, loginPath, strings.NewReader(`{"email":"`+readerEmail+`","password":"wrong"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			f.h.ServeHTTP(rec, req)
			codes <- rec.Code
		}()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for c := range codes {
		counts[c]++
	}
	// 503 means the attempt was counted but waited too long for a password check.
	if counts[http.StatusTooManyRequests] != 8 || counts[http.StatusUnauthorized]+counts[http.StatusServiceUnavailable] != 8 {
		t.Fatalf("status counts = %v, want 8 attempts let through and 8 refused", counts)
	}
}

// Behind trusted proxies, the limits count against the address the outermost proxy added, not
// the entries a client can put in front of it. IPv6 addresses count per /64.
func TestLoginLimitUsesTheAddressTheProxyAdded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		hops   int
		header func(i int) string
	}{
		{"one proxy", 1, func(i int) string { return fmt.Sprintf("10.0.0.%d, 203.0.113.9", i) }},
		{"two proxies", 2, func(i int) string { return fmt.Sprintf("10.0.0.%d, 203.0.113.9, 198.51.100.1", i) }},
		{"IPv6 in one /64", 1, func(i int) string { return fmt.Sprintf("2001:db8:1:2::%x", i+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, func(d *Deps) { d.TrustProxy, d.ProxyHops = true, tc.hops })
			for i := 0; i < 8; i++ {
				if rec := loginFrom(f, tc.header(i), readerEmail, "wrong"); rec.Code != http.StatusUnauthorized {
					t.Fatalf("attempt %d: status %d", i+1, rec.Code)
				}
			}
			if rec := loginFrom(f, tc.header(99), readerEmail, password); rec.Code != http.StatusTooManyRequests {
				t.Fatalf("ninth attempt: status %d, want 429", rec.Code)
			}
		})
	}
}

// Failures spread over many addresses block the account for browsers it does not know, but a
// browser that signed in to it before keeps its own budget, so its owner is not locked out.
func TestAccountLimitSparesARecognisedBrowser(t *testing.T) {
	f := newFixture(t, func(d *Deps) { d.TrustProxy = true })
	first := loginFrom(f, "198.51.100.7", readerEmail, password)
	device := deviceCookie(first)
	if first.Code != http.StatusOK || device == nil {
		t.Fatalf("first sign-in: status %d, device cookie %v", first.Code, device)
	}
	if !device.HttpOnly || device.SameSite != http.SameSiteStrictMode || device.Path != loginPath {
		t.Fatalf("device cookie = %+v, want HttpOnly, SameSite=Strict, Path=%s", device, loginPath)
	}

	for i := 0; i < maxAttemptsPerAccount; i++ {
		if rec := loginFrom(f, fmt.Sprintf("203.0.113.%d", i/7), readerEmail, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i+1, rec.Code)
		}
	}
	if rec := loginFrom(f, "192.0.2.50", readerEmail, password); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("unknown browser: status %d, want 429", rec.Code)
	}
	if rec := loginFrom(f, "192.0.2.50", readerEmail, password, device); rec.Code != http.StatusOK {
		t.Fatalf("recognised browser: status %d, want 200", rec.Code)
	}
}

func TestOneAddressCannotTryManyAccounts(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < maxAttemptsPerAddress; i++ {
		if rec := f.login(fmt.Sprintf("user%d@example.org", i), "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i+1, rec.Code)
		}
	}
	if rec := f.login("another@example.org", "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rec.Code)
	}
}

// Browsers mark cross-origin requests. A page on another origin, a sibling subdomain included,
// cannot change anything with the user's cookie.
func TestCrossOriginWritesAreRefused(t *testing.T) {
	f := newFixture(t)
	admin := adminCookie(t, f)
	send := func(headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", strings.NewReader(`{"email":"new@example.org","role":"user"}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		req.AddCookie(admin)
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		return rec
	}
	for name, headers := range map[string]map[string]string{
		"another site":                     {"Sec-Fetch-Site": "cross-site"},
		"a sibling subdomain":              {"Sec-Fetch-Site": "same-site"},
		"a browser without Fetch Metadata": {"Origin": "https://other.example.org"},
	} {
		if rec := send(headers); rec.Code != http.StatusForbidden || errorCode(t, rec) != "CROSS_ORIGIN" {
			t.Errorf("from %s: status %d body %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := send(map[string]string{"Sec-Fetch-Site": "same-origin"}); rec.Code != http.StatusCreated {
		t.Fatalf("same origin: status %d body %s", rec.Code, rec.Body.String())
	}
}

// A browser sends a text/plain body across origins without asking first, so JSON routes refuse it.
func TestJSONRoutesRefuseOtherContentTypes(t *testing.T) {
	f := newFixture(t)
	admin := adminCookie(t, f)
	for _, path := range []string{"/api/v1/admin/users", loginPath} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"email":"evil@example.org","role":"admin","password":"attacker-chosen-pw"}`))
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.AddCookie(admin)
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType || errorCode(t, rec) != "UNSUPPORTED_MEDIA_TYPE" {
			t.Errorf("%s: status %d body %s", path, rec.Code, rec.Body.String())
		}
	}
	if _, err := f.store.UserByEmail(context.Background(), "evil@example.org"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("user created from a text/plain body: %v", err)
	}
}

func TestSecurityHeaders(t *testing.T) {
	f := newFixture(t)
	api := f.do(http.MethodGet, "/api/v1/auth/oidc/status", nil)
	for name, want := range map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "same-origin",
		"Strict-Transport-Security": "max-age=31536000",
	} {
		if got := api.Header().Get(name); got != want {
			t.Errorf("API %s = %q, want %q", name, got, want)
		}
	}
	if csp := api.Header().Get("Content-Security-Policy"); csp != "" {
		t.Errorf("API response carries the web app's policy: %q", csp)
	}

	page := f.do(http.MethodGet, "/library", nil)
	csp := page.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'self'", "script-src 'self'", "object-src 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("web app policy %q lacks %q", csp, directive)
		}
	}
	if got := page.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("web app X-Frame-Options = %q", got)
	}
}
