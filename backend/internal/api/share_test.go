package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/archivr/backend/internal/auth"
)

type shareLink struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// sharedDoc uploads a document and shares it for the given period.
func sharedDoc(t *testing.T, f *fixture, ttl time.Duration) (id int64, link shareLink) {
	t.Helper()
	c := readerCookie(t, f)
	rec := f.upload(c, "Lease.pdf", pdfData("lease"), "Lease")
	var doc struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, rec, &doc)

	rec = f.do(http.MethodPost, "/api/v1/documents/"+itoa(doc.ID)+"/share",
		map[string]int64{"expires_in_seconds": int64(ttl / time.Second)}, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("share status = %d body %s", rec.Code, rec.Body.String())
	}
	decodeInto(t, rec, &link)
	return doc.ID, link
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestShareLinkOpensDocumentWithoutSignIn(t *testing.T) {
	f := newFixture(t)
	_, link := sharedDoc(t, f, 24*time.Hour)

	if !strings.HasPrefix(link.URL, "https://archive.test/share/") || !strings.HasSuffix(link.URL, link.Token) {
		t.Fatalf("url = %q", link.URL)
	}

	// No cookie, no Authorization header.
	rec := f.do(http.MethodGet, "/api/v1/shares/"+link.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("info status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("headers = %v", rec.Header())
	}
	var info map[string]any
	decodeInto(t, rec, &info)
	if info["title"] != "Lease" || info["filename"] != "10000 - Lease.pdf" || info["mime"] != "application/pdf" {
		t.Fatalf("info = %v", info)
	}
	// Only what the viewer needs: nothing about where the document is filed or what it says.
	for _, leak := range []string{"id", "folder", "folder_id", "tags", "correspondent", "text", "sha256", "source", "source_ref"} {
		if _, ok := info[leak]; ok {
			t.Fatalf("share info leaks %q: %v", leak, info)
		}
	}

	file := f.do(http.MethodGet, "/api/v1/shares/"+link.Token+"/file", nil)
	if file.Code != http.StatusOK || !strings.HasPrefix(file.Body.String(), "%PDF-1.4") {
		t.Fatalf("file status = %d", file.Code)
	}
	if got := file.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline") {
		t.Fatalf("disposition = %q", got)
	}

	dl := f.do(http.MethodGet, "/api/v1/shares/"+link.Token+"/file?download=1", nil)
	if got := dl.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") || !strings.Contains(got, "Lease.pdf") {
		t.Fatalf("download disposition = %q", got)
	}
}

func TestShareLinkGrantsNoOtherAccess(t *testing.T) {
	f := newFixture(t)
	id, link := sharedDoc(t, f, time.Hour)

	// The token is not a session: not as a Bearer token and not as the session cookie.
	for _, path := range []string{"/api/v1/auth/me", "/api/v1/documents", "/api/v1/documents/" + itoa(id), "/api/v1/documents/" + itoa(id) + "/file"} {
		rec := f.do(http.MethodGet, path, nil, &http.Cookie{Name: sessionCookieName, Value: link.Token})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s with the share token as cookie: status %d", path, rec.Code)
		}
	}
	// Creating a share link needs a session.
	rec := f.do(http.MethodPost, "/api/v1/documents/"+itoa(id)+"/share", map[string]int64{"expires_in_seconds": 3600})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous share: status %d", rec.Code)
	}
}

func TestShareLinkExpiresAndRejectsBadTokens(t *testing.T) {
	f := newFixture(t)
	_, link := sharedDoc(t, f, time.Hour)

	f.skew = 2 * time.Hour
	rec := f.do(http.MethodGet, "/api/v1/shares/"+link.Token, nil)
	if rec.Code != http.StatusGone || errorCode(t, rec) != "SHARE_EXPIRED" {
		t.Fatalf("expired info: status %d body %s", rec.Code, rec.Body.String())
	}
	rec = f.do(http.MethodGet, "/api/v1/shares/"+link.Token+"/file", nil)
	if rec.Code != http.StatusGone {
		t.Fatalf("expired file: status %d", rec.Code)
	}

	f.skew = 0
	for _, tok := range []string{"nonsense", link.Token + "x", link.Token[:len(link.Token)-4]} {
		rec := f.do(http.MethodGet, "/api/v1/shares/"+tok, nil)
		if rec.Code != http.StatusNotFound || errorCode(t, rec) != "SHARE_INVALID" {
			t.Fatalf("token %q: status %d body %s", tok, rec.Code, rec.Body.String())
		}
	}
}

func TestShareLinkStopsWorkingWhenDocumentIsDeleted(t *testing.T) {
	f := newFixture(t)
	id, link := sharedDoc(t, f, time.Hour)
	c := readerCookie(t, f)

	if rec := f.do(http.MethodDelete, "/api/v1/documents/"+itoa(id), nil, c); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rec.Code)
	}
	for _, p := range []string{"", "/file"} {
		if rec := f.do(http.MethodGet, "/api/v1/shares/"+link.Token+p, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("deleted document%s: status %d", p, rec.Code)
		}
	}
}

func TestShareValidityIsChecked(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	rec := f.upload(c, "a.pdf", pdfData("a"), "")
	var doc struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, rec, &doc)
	path := "/api/v1/documents/" + itoa(doc.ID) + "/share"

	for _, secs := range []int64{0, -5, 30, int64(auth.MaxShareTTL/time.Second) + 1, 1 << 62} {
		rec := f.do(http.MethodPost, path, map[string]int64{"expires_in_seconds": secs}, c)
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "INVALID" {
			t.Fatalf("expires_in_seconds %d: status %d", secs, rec.Code)
		}
	}
	if rec := f.do(http.MethodPost, "/api/v1/documents/999999/share", map[string]int64{"expires_in_seconds": 3600}, c); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown document: status %d", rec.Code)
	}
}
