package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/archivr/backend/internal/paperless"
)

// fakePaperlessServer answers just enough of the Paperless-ngx API for one document.
func fakePaperlessServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token good-token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/documents/":
			json.NewEncoder(w).Encode(map[string]any{"count": 1, "next": nil, "results": []map[string]any{
				{"id": 7, "title": "Tax return", "created": "2025-04-30", "tags": []int{1}, "document_type": 1,
					"correspondent": 1, "original_file_name": "tax.pdf"},
			}})
		case "/api/correspondents/":
			json.NewEncoder(w).Encode(map[string]any{"count": 1, "next": nil, "results": []map[string]any{{"id": 1, "name": "Tax Office"}}})
		case "/api/tags/":
			json.NewEncoder(w).Encode(map[string]any{"count": 1, "next": nil, "results": []map[string]any{{"id": 1, "name": "tax", "document_count": 4}}})
		case "/api/document_types/":
			json.NewEncoder(w).Encode(map[string]any{"count": 1, "next": nil, "results": []map[string]any{{"id": 1, "name": "Return"}}})
		case "/api/documents/7/download/":
			w.Write(pdfData("tax return"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPaperlessRoutesAreAdminOnly(t *testing.T) {
	f := newFixture(t)
	reader := readerCookie(t, f)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/settings/paperless"},
		{"PUT", "/api/v1/admin/settings/paperless"},
		{"POST", "/api/v1/admin/settings/paperless/test"},
		{"GET", "/api/v1/admin/paperless/import"},
		{"POST", "/api/v1/admin/paperless/import"},
		{"POST", "/api/v1/admin/paperless/import/cancel"},
		{"GET", "/api/v1/admin/paperless/tags"},
		{"GET", "/api/v1/admin/paperless/mapping"},
		{"PUT", "/api/v1/admin/paperless/mapping"},
	} {
		if rec := f.do(c.method, c.path, nil, reader); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as user = %d, want 403", c.method, c.path, rec.Code)
		}
		if rec := f.do(c.method, c.path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s signed out = %d, want 401", c.method, c.path, rec.Code)
		}
	}
}

func TestPaperlessTokenIsMaskedAndKeptWhenMasked(t *testing.T) {
	f := newFixture(t)
	admin := adminCookie(t, f)

	rec := f.do("PUT", "/api/v1/admin/settings/paperless",
		map[string]string{"url": "https://paperless.example.org/api/", "token": "abc123"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "abc123") {
		t.Fatalf("token leaked in the save response: %s", rec.Body.String())
	}
	var v paperlessView
	decodeInto(t, rec, &v)
	if v.URL != "https://paperless.example.org" || !v.TokenSet || v.Token != maskedSecret {
		t.Fatalf("view = %+v", v)
	}

	if v.InsecureTLS {
		t.Fatal("certificate checks should be on by default")
	}

	// Sending the mask back keeps the stored token.
	f.do("PUT", "/api/v1/admin/settings/paperless",
		map[string]any{"url": "https://paperless.example.org", "token": maskedSecret, "insecure_tls": true}, admin)
	got, err := paperless.LoadSettings(t.Context(), f.store)
	if err != nil || got.Token != "abc123" || !got.InsecureTLS {
		t.Fatalf("stored settings = %+v, %v", got, err)
	}
	if rec := f.do("GET", "/api/v1/admin/settings/paperless", nil, admin); strings.Contains(rec.Body.String(), "abc123") {
		t.Fatalf("token leaked in GET: %s", rec.Body.String())
	}

	bad := f.do("PUT", "/api/v1/admin/settings/paperless", map[string]string{"url": "not a url", "token": "x"}, admin)
	if bad.Code != http.StatusBadRequest || errorCode(t, bad) != "INVALID_SETTINGS" {
		t.Fatalf("bad url = %d %s", bad.Code, bad.Body.String())
	}
}

func TestPaperlessTestAndImportEndToEnd(t *testing.T) {
	f := newFixture(t)
	admin := adminCookie(t, f)
	srv := fakePaperlessServer(t)

	// Nothing saved yet.
	if rec := f.do("POST", "/api/v1/admin/settings/paperless/test", nil, admin); rec.Code != http.StatusBadRequest {
		t.Fatalf("test without settings = %d", rec.Code)
	}
	if rec := f.do("POST", "/api/v1/admin/paperless/import", nil, admin); rec.Code != http.StatusBadRequest {
		t.Fatalf("import without settings = %d", rec.Code)
	}

	f.do("PUT", "/api/v1/admin/settings/paperless", map[string]string{"url": srv.URL, "token": "wrong"}, admin)
	rec := f.do("POST", "/api/v1/admin/settings/paperless/test", nil, admin)
	if rec.Code != http.StatusBadGateway || errorCode(t, rec) != "PAPERLESS_FAILED" {
		t.Fatalf("test with a bad token = %d %s", rec.Code, rec.Body.String())
	}

	f.do("PUT", "/api/v1/admin/settings/paperless", map[string]string{"url": srv.URL, "token": "good-token"}, admin)
	rec = f.do("POST", "/api/v1/admin/settings/paperless/test", nil, admin)
	var test struct {
		OK        bool `json:"ok"`
		Documents int  `json:"documents"`
	}
	decodeInto(t, rec, &test)
	if rec.Code != http.StatusOK || !test.OK || test.Documents != 1 {
		t.Fatalf("test = %d %s", rec.Code, rec.Body.String())
	}

	if rec := f.do("POST", "/api/v1/admin/paperless/import", map[string]any{"folder_id": 999}, admin); rec.Code != http.StatusBadRequest {
		t.Fatalf("import into an unknown folder = %d %s", rec.Code, rec.Body.String())
	}

	rec = f.do("POST", "/api/v1/admin/paperless/import", nil, admin)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	var prog paperless.Progress
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		decodeInto(t, f.do("GET", "/api/v1/admin/paperless/import", nil, admin), &prog)
		if prog.State != paperless.StateRunning {
			break
		}
	}
	if prog.State != paperless.StateDone || prog.Imported != 1 {
		t.Fatalf("progress = %+v", prog)
	}

	var list struct {
		Items []struct {
			Title  string   `json:"title"`
			Source string   `json:"source"`
			Tags   []string `json:"tags"`

			Correspondent string `json:"correspondent"`
		} `json:"items"`
	}
	decodeInto(t, f.do("GET", "/api/v1/documents", nil, admin), &list)
	if len(list.Items) != 1 || list.Items[0].Title != "Tax return" || list.Items[0].Source != "paperless" ||
		list.Items[0].Correspondent != "Tax Office" || strings.Join(list.Items[0].Tags, ",") != "tax" {
		t.Fatalf("documents = %+v", list.Items)
	}
}

func TestPaperlessMappingEndpoints(t *testing.T) {
	f := newFixture(t)
	admin := adminCookie(t, f)

	if rec := f.do("GET", "/api/v1/admin/paperless/mapping", nil, readerCookie(t, f)); rec.Code != http.StatusForbidden {
		t.Fatalf("mapping as user = %d", rec.Code)
	}

	var m paperless.Mapping
	decodeInto(t, f.do("GET", "/api/v1/admin/paperless/mapping", nil, admin), &m)
	if m.Rules == nil || len(m.Rules) != 0 {
		t.Fatalf("empty mapping should be an empty list, got %+v", m)
	}

	folder := decodeFolder(t, f.do("POST", "/api/v1/folders", map[string]any{"name": "Housing"}, admin))
	rules := func(r ...paperless.TagFolder) map[string]any { return map[string]any{"rules": r} }

	rec := f.do("PUT", "/api/v1/admin/paperless/mapping",
		rules(paperless.TagFolder{Tag: "  lease ", FolderID: folder}, paperless.TagFolder{Tag: "tax", FolderID: folder}), admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	decodeInto(t, f.do("GET", "/api/v1/admin/paperless/mapping", nil, admin), &m)
	if len(m.Rules) != 2 || m.Rules[0].Tag != "lease" || m.Rules[1].Tag != "tax" {
		t.Fatalf("saved rules = %+v", m.Rules)
	}

	for name, body := range map[string]map[string]any{
		"duplicate tag": rules(paperless.TagFolder{Tag: "a", FolderID: folder}, paperless.TagFolder{Tag: "A", FolderID: folder}),
		"no folder":     rules(paperless.TagFolder{Tag: "a"}),
		"no tag":        rules(paperless.TagFolder{FolderID: folder}),
	} {
		if rec := f.do("PUT", "/api/v1/admin/paperless/mapping", body, admin); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "INVALID_MAPPING" {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec = f.do("PUT", "/api/v1/admin/paperless/mapping", rules(paperless.TagFolder{Tag: "a", FolderID: 9999}), admin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no longer exists") {
		t.Errorf("unknown folder = %d %s", rec.Code, rec.Body.String())
	}
	// A rejected save leaves the old mapping alone.
	decodeInto(t, f.do("GET", "/api/v1/admin/paperless/mapping", nil, admin), &m)
	if len(m.Rules) != 2 {
		t.Fatalf("mapping changed by a rejected save: %+v", m.Rules)
	}
}

func decodeFolder(t *testing.T, rec *httptest.ResponseRecorder) int64 {
	t.Helper()
	var fo struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, rec, &fo)
	if rec.Code != http.StatusCreated || fo.ID == 0 {
		t.Fatalf("create folder = %d %s", rec.Code, rec.Body.String())
	}
	return fo.ID
}

func TestPaperlessTagsEndpoint(t *testing.T) {
	f := newFixture(t)
	admin := adminCookie(t, f)
	if rec := f.do("GET", "/api/v1/admin/paperless/tags", nil, admin); rec.Code != http.StatusBadRequest {
		t.Fatalf("tags without settings = %d", rec.Code)
	}
	srv := fakePaperlessServer(t)
	f.do("PUT", "/api/v1/admin/settings/paperless", map[string]string{"url": srv.URL, "token": "good-token"}, admin)
	rec := f.do("GET", "/api/v1/admin/paperless/tags", nil, admin)
	var out struct {
		Items []paperless.TagInfo `json:"items"`
	}
	decodeInto(t, rec, &out)
	if rec.Code != http.StatusOK || len(out.Items) != 1 || out.Items[0].Name != "tax" || out.Items[0].DocumentCount != 4 {
		t.Fatalf("tags = %d %s", rec.Code, rec.Body.String())
	}
}
