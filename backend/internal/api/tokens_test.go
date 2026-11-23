package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/archivr/backend/internal/auth"
)

type tokenJSON struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	LastUsedAt *string `json:"last_used_at"`
	ExpiresAt  *string `json:"expires_at"`
	Token      string  `json:"token"`
}

func (f *fixture) newToken(c *http.Cookie, name string, days *int) tokenJSON {
	f.t.Helper()
	body := map[string]any{"name": name}
	if days != nil {
		body["expires_in_days"] = *days
	}
	rec := f.do(http.MethodPost, "/api/v1/tokens", body, c)
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("create token: %d %s", rec.Code, rec.Body.String())
	}
	var tok tokenJSON
	decodeInto(f.t, rec, &tok)
	return tok
}

func (f *fixture) withBearer(method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func TestTokenIsShownOnceAndStoredHashed(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	tok := f.newToken(c, "laptop", nil)
	if !strings.HasPrefix(tok.Token, auth.TokenPrefix) || len(tok.Token) < 40 {
		t.Fatalf("token = %q", tok.Token)
	}
	if !strings.HasPrefix(tok.Token, tok.Prefix) || tok.Prefix == tok.Token {
		t.Fatalf("prefix %q does not start token %q or gives it away", tok.Prefix, tok.Token)
	}
	if tok.ExpiresAt != nil {
		t.Fatalf("expires_at = %v for a token with no expiry", *tok.ExpiresAt)
	}

	rec := f.do(http.MethodGet, "/api/v1/tokens", nil, c)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), tok.Token) {
		t.Fatalf("list: %d, and it must not repeat the secret: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []tokenJSON `json:"items"`
	}
	decodeInto(t, rec, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "laptop" || list.Items[0].Prefix != tok.Prefix {
		t.Fatalf("list = %+v", list.Items)
	}

	var stored string
	if err := f.store.DB.QueryRowContext(context.Background(),
		`SELECT token_hash FROM api_tokens WHERE id = ?`, tok.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == tok.Token || stored != auth.HashToken(tok.Token) {
		t.Fatalf("stored %q: only the hash of the token may be kept", stored)
	}
}

func TestTokenListIsPerUser(t *testing.T) {
	f := newFixture(t)
	reader, admin := readerCookie(t, f), adminCookie(t, f)
	mine := f.newToken(reader, "mine", nil)

	rec := f.do(http.MethodGet, "/api/v1/tokens", nil, admin)
	if strings.Contains(rec.Body.String(), "mine") {
		t.Fatalf("admin sees the reader's token: %s", rec.Body.String())
	}
	if rec := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/tokens/%d", mine.ID), nil, admin); rec.Code != http.StatusNotFound {
		t.Fatalf("revoking another user's token: %d", rec.Code)
	}
	if rec := f.withBearer(http.MethodPost, "/api/v1/mcp", mine.Token); rec.Code == http.StatusUnauthorized {
		t.Fatal("the token stopped working after another user tried to revoke it")
	}
}

func TestTokenCreateValidatesInput(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	zero, huge := 0, 4000
	for name, body := range map[string]map[string]any{
		"no name":         {"name": "  "},
		"long name":       {"name": strings.Repeat("x", 101)},
		"zero days":       {"name": "a", "expires_in_days": zero},
		"too many days":   {"name": "a", "expires_in_days": huge},
		"negative expiry": {"name": "a", "expires_in_days": -1},
	} {
		if rec := f.do(http.MethodPost, "/api/v1/tokens", body, c); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if rec := f.do(http.MethodPost, "/api/v1/tokens", map[string]any{"name": "ok"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous create: %d", rec.Code)
	}
}

func TestTokenCannotManageTokensOrOpenOtherRoutes(t *testing.T) {
	f := newFixture(t)
	tok := f.newToken(readerCookie(t, f), "tool", nil)
	for _, path := range []string{"/api/v1/tokens", "/api/v1/auth/me", "/api/v1/documents", "/api/v1/folders"} {
		if rec := f.withBearer(http.MethodGet, path, tok.Token); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with a token: %d, want 401", path, rec.Code)
		}
	}
}

func TestTokenRevokeAndExpiry(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	days := 2
	short := f.newToken(c, "short", &days)
	if short.ExpiresAt == nil {
		t.Fatal("no expires_at")
	}
	long := f.newToken(c, "long", nil)

	for _, tok := range []tokenJSON{short, long} {
		if rec := f.withBearer(http.MethodPost, "/api/v1/mcp", tok.Token); rec.Code == http.StatusUnauthorized {
			t.Fatalf("%s token refused", tok.Name)
		}
	}

	f.skew = 3 * 24 * time.Hour
	if rec := f.withBearer(http.MethodPost, "/api/v1/mcp", short.Token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: %d", rec.Code)
	}
	f.skew = 0

	if rec := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/tokens/%d", long.ID), nil, c); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if rec := f.withBearer(http.MethodPost, "/api/v1/mcp", long.Token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", rec.Code)
	}
	if rec := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/tokens/%d", long.ID), nil, c); rec.Code != http.StatusNotFound {
		t.Fatalf("revoking twice: %d", rec.Code)
	}
}

func TestTokenLastUsedIsRecorded(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	tok := f.newToken(c, "tool", nil)
	f.withBearer(http.MethodPost, "/api/v1/mcp", tok.Token)

	var list struct {
		Items []tokenJSON `json:"items"`
	}
	decodeInto(t, f.do(http.MethodGet, "/api/v1/tokens", nil, c), &list)
	if len(list.Items) != 1 || list.Items[0].LastUsedAt == nil {
		t.Fatalf("last_used_at not set: %+v", list.Items)
	}
}

func TestDeletedUsersTokenStops(t *testing.T) {
	f := newFixture(t)
	reader := f.newToken(readerCookie(t, f), "tool", nil)
	user, err := f.store.UserByEmail(context.Background(), readerEmail)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteUser(context.Background(), user.ID); err != nil {
		t.Fatal(err)
	}
	if rec := f.withBearer(http.MethodPost, "/api/v1/mcp", reader.Token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token of a deleted user: %d", rec.Code)
	}
}

func TestMCPRequiresAToken(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)

	jwt := ""
	rec := f.login(readerEmail, password)
	var body struct {
		Token string `json:"token"`
	}
	decodeInto(t, rec, &body)
	jwt = body.Token

	cases := map[string]func(*http.Request){
		"no credentials": func(*http.Request) {},
		"session cookie": func(r *http.Request) { r.AddCookie(c) },
		"session JWT":    func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+jwt) },
		"unknown token":  func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+auth.TokenPrefix+"nope") },
		"wrong scheme":   func(r *http.Request) { r.Header.Set("Authorization", "Basic abc") },
	}
	for name, set := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		set(req)
		out := httptest.NewRecorder()
		f.h.ServeHTTP(out, req)
		if out.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, out.Code)
		}
		if out.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: no WWW-Authenticate header", name)
		}
	}
}
