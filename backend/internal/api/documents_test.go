package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/archivr/backend/internal/store"
)

// upload sends a multipart upload as the signed-in user.
func (f *fixture) upload(c *http.Cookie, filename string, data []byte, title string) *httptest.ResponseRecorder {
	f.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		f.t.Fatal(err)
	}
	fw.Write(data)
	if title != "" {
		mw.WriteField("title", title)
	}
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func pdfData(label string) []byte {
	return []byte("%PDF-1.4\n% " + label + "\n%%EOF\n")
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func adminCookie(t *testing.T, f *fixture) *http.Cookie {
	t.Helper()
	return sessionCookie(f.login(adminEmail, password))
}

func readerCookie(t *testing.T, f *fixture) *http.Cookie {
	t.Helper()
	return sessionCookie(f.login(readerEmail, password))
}

func TestUploadFilesIntoInboxAndRejectsDuplicates(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)

	rec := f.upload(c, "Electric bill.pdf", pdfData("electric"), "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body %s", rec.Code, rec.Body.String())
	}
	var doc struct {
		ID     int64  `json:"id"`
		Title  string `json:"title"`
		Folder struct {
			Path string `json:"path"`
		} `json:"folder"`
	}
	decodeInto(t, rec, &doc)
	if doc.ID != 10000 || doc.Title != "Electric bill" || doc.Folder.Path != "INBOX" {
		t.Fatalf("doc = %+v", doc)
	}

	dup := f.upload(c, "copy.pdf", pdfData("electric"), "")
	if dup.Code != http.StatusConflict || errorCode(t, dup) != "DUPLICATE" {
		t.Fatalf("duplicate upload: status %d", dup.Code)
	}
	if !strings.Contains(dup.Body.String(), "10000") {
		t.Fatalf("duplicate message should name the existing number: %s", dup.Body.String())
	}

	// The same file under the same name is still a duplicate, and says which number it is.
	again := f.upload(c, "Electric bill.pdf", pdfData("electric"), "")
	if again.Code != http.StatusConflict || errorCode(t, again) != "DUPLICATE" {
		t.Fatalf("re-upload: status %d body %s", again.Code, again.Body.String())
	}
	if !strings.Contains(again.Body.String(), "10000") {
		t.Fatalf("re-upload message should name No. 10000: %s", again.Body.String())
	}

	bad := f.upload(c, "notes.txt", []byte("just text"), "")
	if bad.Code != http.StatusUnsupportedMediaType || errorCode(t, bad) != "UNSUPPORTED_TYPE" {
		t.Fatalf("text upload: status %d", bad.Code)
	}
}

func TestSearchFindsOCRTextAndSuggestsDates(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	rec := f.upload(c, "letter.pdf", pdfData("letter"), "Landlord letter")
	var doc struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, rec, &doc)

	// Stand in for the OCR worker: store text and refresh the search index.
	ctx := context.Background()
	if err := f.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SaveOCRText(ctx, doc.ID, "Rent review dated 14 octobre 2026.\nTerms of the lease.", "tesseract", "fra"); err != nil {
			return err
		}
		return tx.SyncFTS(ctx, doc.ID)
	}); err != nil {
		t.Fatal(err)
	}

	search := f.do(http.MethodGet, "/api/v1/documents?q=lease", nil, c)
	var list struct {
		Items []struct {
			ID      int64  `json:"id"`
			Snippet string `json:"snippet"`
		} `json:"items"`
		Total int `json:"total"`
	}
	decodeInto(t, search, &list)
	if list.Total != 1 || list.Items[0].ID != doc.ID || !strings.Contains(list.Items[0].Snippet, "\x01") {
		t.Fatalf("search = %+v", list)
	}

	sug := f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents/%d/suggestions", doc.ID), nil, c)
	var s struct {
		Dates []struct {
			Value string `json:"value"`
			Label string `json:"label"`
		} `json:"dates"`
	}
	decodeInto(t, sug, &s)
	if len(s.Dates) != 1 || s.Dates[0].Value != "2026-10-14" || s.Dates[0].Label != "14 Oct 2026" {
		t.Fatalf("suggestions = %+v", s)
	}
}

func TestFilingUpdatesDocumentAndDeleteRemovesIt(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	rec := f.upload(c, "scan.pdf", pdfData("filing"), "Scan")
	var doc struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, rec, &doc)

	folderRec := f.do(http.MethodPost, "/api/v1/folders", map[string]any{"name": "Finance"}, c)
	if folderRec.Code != http.StatusCreated {
		t.Fatalf("create folder: %d %s", folderRec.Code, folderRec.Body.String())
	}
	var folder struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, folderRec, &folder)

	admin := adminCookie(t, f)
	typeRec := f.do(http.MethodPost, "/api/v1/types", map[string]any{"name": "Invoice", "color": "#1F4E79"}, admin)
	if typeRec.Code != http.StatusCreated {
		t.Fatalf("create type: %d %s", typeRec.Code, typeRec.Body.String())
	}
	var typ struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, typeRec, &typ)

	patch := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/documents/%d", doc.ID), map[string]any{
		"title":         "Electric bill",
		"document_date": "2026-10-14",
		"type_id":       typ.ID,
		"correspondent": "  City   Power ",
		"tags":          []string{"utilities", "2026"},
		"folder_id":     folder.ID,
	}, c)
	if patch.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", patch.Code, patch.Body.String())
	}
	var filed struct {
		Folder struct {
			Path string `json:"path"`
		} `json:"folder"`
		Tags []string `json:"tags"`
		Type struct {
			Name string `json:"name"`
		} `json:"type"`
		Correspondent string `json:"correspondent"`
	}
	decodeInto(t, patch, &filed)
	if filed.Correspondent != "City Power" {
		t.Fatalf("correspondent = %q", filed.Correspondent)
	}
	if filed.Folder.Path != "Finance" || len(filed.Tags) != 2 || filed.Type.Name != "Invoice" {
		t.Fatalf("filed = %+v", filed)
	}

	// The file is served inline with the right type.
	file := f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents/%d/file", doc.ID), nil, c)
	if file.Code != http.StatusOK || file.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("file: %d type %q", file.Code, file.Header().Get("Content-Type"))
	}
	if !strings.HasPrefix(file.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("disposition = %q", file.Header().Get("Content-Disposition"))
	}
	if !bytes.HasPrefix(file.Body.Bytes(), []byte("%PDF-")) {
		t.Fatal("served body is not the PDF")
	}

	// A folder that still holds documents cannot be deleted.
	if del := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/folders/%d", folder.ID), nil, c); del.Code != http.StatusConflict {
		t.Fatalf("delete non-empty folder: %d", del.Code)
	}

	if del := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/documents/%d", doc.ID), nil, c); del.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", del.Code)
	}
	if get := f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents/%d", doc.ID), nil, c); get.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", get.Code)
	}
}

// A correspondent typed on a document is added to the list once. Another document
// that names it in a different case shares the entry, and the list shows the count.
func TestCorrespondentIsAddedToListOnce(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)

	var ids [2]int64
	for i, data := range [][]byte{pdfData("landlord-1"), pdfData("landlord-2")} {
		rec := f.upload(c, fmt.Sprintf("letter-%d.pdf", i), data, "Letter")
		var doc struct {
			ID int64 `json:"id"`
		}
		decodeInto(t, rec, &doc)
		typed := []string{"Landlord Ltd", "landlord ltd"}[i]
		patch := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/documents/%d", doc.ID), map[string]any{"correspondent": typed}, c)
		if patch.Code != http.StatusOK {
			t.Fatalf("patch %d: %d %s", i, patch.Code, patch.Body.String())
		}
		var filed struct {
			CorrespondentID *int64 `json:"correspondent_id"`
			Correspondent   string `json:"correspondent"`
		}
		decodeInto(t, patch, &filed)
		// Both spellings resolve to the entry that was added first.
		if filed.CorrespondentID == nil || filed.Correspondent != "Landlord Ltd" {
			t.Fatalf("doc %d: correspondent = %q (%v)", i, filed.Correspondent, filed.CorrespondentID)
		}
		ids[i] = *filed.CorrespondentID
	}
	if ids[0] != ids[1] {
		t.Fatalf("same correspondent in two spellings got IDs %d and %d", ids[0], ids[1])
	}

	list := f.do(http.MethodGet, "/api/v1/correspondents", nil, c)
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	var page struct {
		Items []struct {
			Name      string `json:"name"`
			Documents int    `json:"document_count"`
		} `json:"items"`
	}
	decodeInto(t, list, &page)
	if len(page.Items) != 1 || page.Items[0].Name != "Landlord Ltd" || page.Items[0].Documents != 2 {
		t.Fatalf("list = %+v", page.Items)
	}

	// The search filter matches part of a name, ignoring case.
	found := f.do(http.MethodGet, "/api/v1/correspondents?q=LAND", nil, c)
	decodeInto(t, found, &page)
	if len(page.Items) != 1 {
		t.Fatalf("filter by LAND = %+v, want one entry", page.Items)
	}
	none := f.do(http.MethodGet, "/api/v1/correspondents?q=city", nil, c)
	decodeInto(t, none, &page)
	if len(page.Items) != 0 {
		t.Fatalf("filter by city = %+v, want none", page.Items)
	}
}

// A type typed by name on a document is created when it is new. A second spelling in
// a different case reuses it, and an empty name clears it.
func TestTypeTypedByNameIsCreatedOnce(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)

	var ids [2]int64
	for i, data := range [][]byte{pdfData("type-1"), pdfData("type-2")} {
		rec := f.upload(c, fmt.Sprintf("doc-%d.pdf", i), data, "Doc")
		var doc struct {
			ID int64 `json:"id"`
		}
		decodeInto(t, rec, &doc)
		typed := []string{"  Bank   statement ", "bank statement"}[i]
		patch := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/documents/%d", doc.ID), map[string]any{"type": typed}, c)
		if patch.Code != http.StatusOK {
			t.Fatalf("patch %d: %d %s", i, patch.Code, patch.Body.String())
		}
		var filed struct {
			TypeID *int64 `json:"type_id"`
			Type   *struct {
				Name string `json:"name"`
			} `json:"type"`
		}
		decodeInto(t, patch, &filed)
		if filed.TypeID == nil || filed.Type == nil || filed.Type.Name != "Bank statement" {
			t.Fatalf("doc %d: type = %+v (%v)", i, filed.Type, filed.TypeID)
		}
		ids[i] = *filed.TypeID

		if i == 1 {
			cleared := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/documents/%d", doc.ID), map[string]any{"type": ""}, c)
			var after struct {
				TypeID *int64 `json:"type_id"`
			}
			decodeInto(t, cleared, &after)
			if cleared.Code != http.StatusOK || after.TypeID != nil {
				t.Fatalf("clear: %d %s", cleared.Code, cleared.Body.String())
			}
		}
	}
	if ids[0] != ids[1] {
		t.Fatalf("same type in two spellings got IDs %d and %d", ids[0], ids[1])
	}

	list := f.do(http.MethodGet, "/api/v1/types", nil, c)
	var page struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	decodeInto(t, list, &page)
	if len(page.Items) != 1 {
		t.Fatalf("types = %+v, want one", page.Items)
	}
}

func TestPatchRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	rec := f.upload(c, "x.pdf", pdfData("bad"), "")
	var doc struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, rec, &doc)
	url := fmt.Sprintf("/api/v1/documents/%d", doc.ID)

	for name, body := range map[string]map[string]any{
		"bad date":     {"document_date": "14/10/2026"},
		"empty title":  {"title": "   "},
		"unknown type": {"type_id": 999},
		"long sender":  {"correspondent": strings.Repeat("x", 201)},
	} {
		if r := f.do(http.MethodPatch, url, body, c); r.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.Code)
		}
	}
}

func TestOnlyAdminsManageTypesAndUsers(t *testing.T) {
	f := newFixture(t)
	reader := readerCookie(t, f)
	admin := adminCookie(t, f)

	if r := f.do(http.MethodPost, "/api/v1/types", map[string]any{"name": "Nope"}, reader); r.Code != http.StatusForbidden {
		t.Fatalf("reader created a type: %d", r.Code)
	}
	if r := f.do(http.MethodGet, "/api/v1/admin/users", nil, reader); r.Code != http.StatusForbidden {
		t.Fatalf("reader listed users: %d", r.Code)
	}
	if r := f.do(http.MethodGet, "/api/v1/admin/users", nil, admin); r.Code != http.StatusOK {
		t.Fatalf("admin listed users: %d", r.Code)
	}
}

func TestAdminCannotDeleteOwnAccountOrLastAdmin(t *testing.T) {
	f := newFixture(t)
	admin := adminCookie(t, f)
	var me struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, f.do(http.MethodGet, "/api/v1/auth/me", nil, admin), &me)

	if r := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/users/%d", me.ID), nil, admin); r.Code != http.StatusBadRequest {
		t.Fatalf("self delete: %d", r.Code)
	}
	if r := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/users/%d", me.ID), map[string]any{"role": "user"}, admin); r.Code != http.StatusBadRequest {
		t.Fatalf("self demote: %d", r.Code)
	}
}

func TestCreateUserAndSetPassword(t *testing.T) {
	f := newFixture(t)
	admin := adminCookie(t, f)

	r := f.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"email": "Guest@Example.org", "name": "Guest", "role": "user", "password": "a long enough password",
	}, admin)
	if r.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", r.Code, r.Body.String())
	}
	if bytes.Contains(r.Body.Bytes(), []byte("$argon2id$")) {
		t.Fatal("password hash leaked in the response")
	}
	if r := f.do(http.MethodPost, "/api/v1/admin/users", map[string]any{
		"email": "short@example.org", "role": "user", "password": "short",
	}, admin); r.Code != http.StatusBadRequest {
		t.Fatalf("short password accepted: %d", r.Code)
	}
	if login := f.login("guest@example.org", "a long enough password"); login.Code != http.StatusOK {
		t.Fatalf("new user cannot sign in: %d", login.Code)
	}
}

func TestFolderTreeAndInboxCount(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	f.upload(c, "a.pdf", pdfData("count"), "")

	var count struct {
		Count int `json:"count"`
	}
	decodeInto(t, f.do(http.MethodGet, "/api/v1/inbox/count", nil, c), &count)
	if count.Count != 1 {
		t.Fatalf("inbox count = %d, want 1", count.Count)
	}

	parent := f.do(http.MethodPost, "/api/v1/folders", map[string]any{"name": "Home"}, c)
	var p struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, parent, &p)
	child := f.do(http.MethodPost, "/api/v1/folders", map[string]any{"name": "Lease", "parent_id": p.ID}, c)
	if child.Code != http.StatusCreated {
		t.Fatalf("child folder: %d %s", child.Code, child.Body.String())
	}
	var list struct {
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	decodeInto(t, f.do(http.MethodGet, "/api/v1/folders", nil, c), &list)
	paths := []string{}
	for _, it := range list.Items {
		paths = append(paths, it.Path)
	}
	if strings.Join(paths, ",") != "Home,Home/Lease,INBOX" {
		t.Fatalf("folder paths = %v", paths)
	}
}

func TestThumbnailForImage(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)

	img := image.NewRGBA(image.Rect(0, 0, 900, 300))
	for x := 0; x < 900; x++ {
		img.Set(x, 150, color.Black)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	rec := f.upload(c, "photo.png", buf.Bytes(), "Photo")
	var doc struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, rec, &doc)

	th := f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents/%d/thumbnail", doc.ID), nil, c)
	if th.Code != http.StatusOK || th.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumbnail: %d %q", th.Code, th.Header().Get("Content-Type"))
	}
}

func TestUnknownAPIPathIsJSON404(t *testing.T) {
	f := newFixture(t)
	r := f.do(http.MethodGet, "/api/v1/nope", nil)
	if r.Code != http.StatusNotFound || errorCode(t, r) != "NOT_FOUND" {
		t.Fatalf("status %d body %s", r.Code, r.Body.String())
	}
}

func TestDocumentRoutesNeedSignIn(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/api/v1/documents", "/api/v1/folders", "/api/v1/inbox/count", "/api/v1/admin/users"} {
		if r := f.do(http.MethodGet, path, nil); r.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous status = %d", path, r.Code)
		}
	}
}
