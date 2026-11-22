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

	"github.com/archivr/backend/internal/archive"
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

func TestRunMissingOCRQueuesOnlyDocumentsWithoutText(t *testing.T) {
	f := newFixture(t)
	reader := readerCookie(t, f)
	admin := adminCookie(t, f)
	ctx := context.Background()

	ids := map[string]int64{}
	for _, name := range []string{"failed", "skipped", "good", "empty"} {
		var doc struct {
			ID int64 `json:"id"`
		}
		decodeInto(t, f.upload(reader, name+".pdf", pdfData(name), ""), &doc)
		ids[name] = doc.ID
	}
	if err := f.store.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SetOCRStatus(ctx, ids["failed"], "failed", "boom"); err != nil {
			return err
		}
		if err := tx.SetOCRStatus(ctx, ids["skipped"], "skipped", "no tesseract"); err != nil {
			return err
		}
		if err := tx.SaveOCRText(ctx, ids["good"], "some text", "tesseract", "eng"); err != nil {
			return err
		}
		return tx.SetOCRStatus(ctx, ids["empty"], "done", "")
	}); err != nil {
		t.Fatal(err)
	}

	if rec := f.do(http.MethodPost, "/api/v1/admin/ocr/run-missing", nil, reader); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d", rec.Code)
	}

	rec := f.do(http.MethodPost, "/api/v1/admin/ocr/run-missing", nil, admin)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Queued int `json:"queued"`
	}
	decodeInto(t, rec, &out)
	if out.Queued != 3 {
		t.Fatalf("queued = %d, want 3", out.Queued)
	}

	status := func(name string) string {
		doc, err := f.store.GetDocument(ctx, ids[name])
		if err != nil {
			t.Fatal(err)
		}
		return doc.OCRStatus
	}
	for name, want := range map[string]string{"failed": "pending", "skipped": "pending", "empty": "pending", "good": "done"} {
		if got := status(name); got != want {
			t.Errorf("%s status = %q, want %q", name, got, want)
		}
	}

	again := f.do(http.MethodPost, "/api/v1/admin/ocr/run-missing", nil, admin)
	decodeInto(t, again, &out)
	if out.Queued != 0 {
		t.Fatalf("second run queued %d, want 0", out.Queued)
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
	if strings.Join(paths, ",") != "Home,Home/Lease,INBOX,Trash" {
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

func TestReplacedFileGetsANewThumbnail(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)

	solid := func(col color.Color) []byte {
		img := image.NewRGBA(image.Rect(0, 0, 200, 100))
		for x := 0; x < 200; x++ {
			for y := 0; y < 100; y++ {
				img.Set(x, y, col)
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	var doc struct {
		ID     int64  `json:"id"`
		SHA256 string `json:"sha256"`
	}
	decodeInto(t, f.upload(c, "a.png", solid(color.Black), "Photo"), &doc)
	thumb := func() []byte {
		th := f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents/%d/thumbnail", doc.ID), nil, c)
		if th.Code != http.StatusOK {
			t.Fatalf("thumbnail: %d", th.Code)
		}
		return th.Body.Bytes()
	}
	before := thumb()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "b.png")
	fw.Write(solid(color.White))
	mw.Close()
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/documents/%d/file", doc.ID), &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body.String())
	}

	var list struct {
		Items []struct {
			ID     int64  `json:"id"`
			SHA256 string `json:"sha256"`
		} `json:"items"`
	}
	decodeInto(t, f.do(http.MethodGet, "/api/v1/documents", nil, c), &list)
	if len(list.Items) != 1 || list.Items[0].ID != doc.ID || list.Items[0].SHA256 == doc.SHA256 {
		t.Fatalf("list after replace = %+v, old sha %s", list.Items, doc.SHA256)
	}
	if bytes.Equal(before, thumb()) {
		t.Fatal("thumbnail is still the one of the replaced file")
	}
}

// Moving a document into Trash takes it out of the library and out of search.
// Opening the Trash folder lists it again.
func TestTrashStaysOutOfTheLibraryUntilOpened(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	rec := f.upload(c, "receipt.pdf", pdfData("receipt"), "Shop receipt")
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	var doc struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, rec, &doc)

	trash, err := f.store.FolderByPath(context.Background(), archive.TrashName)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		Total int `json:"total"`
	}
	before := f.do(http.MethodGet, "/api/v1/documents?q=receipt", nil, c)
	decodeInto(t, before, &list)
	if list.Total != 1 || list.Items[0].ID != doc.ID {
		t.Fatalf("search before trash = %+v, want document %d", list, doc.ID)
	}

	moved := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/documents/%d", doc.ID), map[string]any{"folder_id": trash.ID}, c)
	if moved.Code != http.StatusOK {
		t.Fatalf("move to trash: %d %s", moved.Code, moved.Body.String())
	}

	all := f.do(http.MethodGet, "/api/v1/documents", nil, c)
	decodeInto(t, all, &list)
	if list.Total != 0 || len(list.Items) != 0 {
		t.Fatalf("library still lists the trashed document: %+v", list)
	}
	search := f.do(http.MethodGet, "/api/v1/documents?q=receipt", nil, c)
	decodeInto(t, search, &list)
	if list.Total != 0 {
		t.Fatalf("search still finds the trashed document: %+v", list)
	}

	inTrash := f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents?folder=%d", trash.ID), nil, c)
	decodeInto(t, inTrash, &list)
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != doc.ID {
		t.Fatalf("trash folder = %+v, want document %d", list, doc.ID)
	}
}

// Empty Trash removes every document in the Trash folder and leaves the rest filed.
func TestEmptyTrashDeletesOnlyWhatIsInTrash(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)

	uploadID := func(name, title string) int64 {
		t.Helper()
		rec := f.upload(c, name, pdfData(title), title)
		if rec.Code != http.StatusCreated {
			t.Fatalf("upload %s: %d %s", name, rec.Code, rec.Body.String())
		}
		var doc struct {
			ID int64 `json:"id"`
		}
		decodeInto(t, rec, &doc)
		return doc.ID
	}
	keep := uploadID("keep.pdf", "Keep this")
	goneA := uploadID("a.pdf", "Throw A")
	goneB := uploadID("b.pdf", "Throw B")

	trash, err := f.store.FolderByPath(context.Background(), archive.TrashName)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{keep, goneA, goneB} {
		moved := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/documents/%d", id), map[string]any{"folder_id": trash.ID}, c)
		if moved.Code != http.StatusOK {
			t.Fatalf("move %d to trash: %d %s", id, moved.Code, moved.Body.String())
		}
	}
	// Restored before emptying, so it must survive.
	restored := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/documents/%d", keep), map[string]any{"folder_id": archive.InboxID}, c)
	if restored.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", restored.Code, restored.Body.String())
	}

	if anon := f.do(http.MethodPost, "/api/v1/trash/empty", nil); anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", anon.Code)
	}
	if reader := f.do(http.MethodPost, "/api/v1/trash/empty", nil, c); reader.Code != http.StatusForbidden {
		t.Fatalf("regular user status = %d, want 403", reader.Code)
	}

	admin := adminCookie(t, f)
	emptied := f.do(http.MethodPost, "/api/v1/trash/empty", nil, admin)
	if emptied.Code != http.StatusOK {
		t.Fatalf("empty trash: %d %s", emptied.Code, emptied.Body.String())
	}
	var result struct {
		Deleted int `json:"deleted"`
	}
	decodeInto(t, emptied, &result)
	if result.Deleted != 2 {
		t.Fatalf("deleted = %d, want 2", result.Deleted)
	}

	if got := f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents/%d", keep), nil, c); got.Code != http.StatusOK {
		t.Fatalf("restored document: %d %s", got.Code, got.Body.String())
	}
	for _, id := range []int64{goneA, goneB} {
		if got := f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents/%d", id), nil, c); got.Code != http.StatusNotFound {
			t.Fatalf("document %d after empty: %d %s", id, got.Code, got.Body.String())
		}
	}

	var list struct {
		Total int `json:"total"`
	}
	inTrash := f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents?folder=%d", trash.ID), nil, c)
	decodeInto(t, inTrash, &list)
	if list.Total != 0 {
		t.Fatalf("trash still holds %d documents", list.Total)
	}

	again := f.do(http.MethodPost, "/api/v1/trash/empty", nil, admin)
	decodeInto(t, again, &result)
	if again.Code != http.StatusOK || result.Deleted != 0 {
		t.Fatalf("second empty = %d %+v", again.Code, result)
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

func TestFavoritesArePerUser(t *testing.T) {
	f := newFixture(t)
	reader := readerCookie(t, f)
	admin := adminCookie(t, f)

	var doc struct {
		ID int64 `json:"id"`
	}
	rec := f.upload(reader, "Lease.pdf", pdfData("lease"), "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body %s", rec.Code, rec.Body.String())
	}
	decodeInto(t, rec, &doc)
	path := fmt.Sprintf("/api/v1/documents/%d/favorite", doc.ID)

	type page struct {
		Items []struct {
			ID       int64 `json:"id"`
			Favorite bool  `json:"favorite"`
		} `json:"items"`
	}
	favorites := func(c *http.Cookie) page {
		t.Helper()
		rec := f.do(http.MethodGet, "/api/v1/documents?favorite=1", nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("list status = %d body %s", rec.Code, rec.Body.String())
		}
		var p page
		decodeInto(t, rec, &p)
		return p
	}

	if got := favorites(reader); len(got.Items) != 0 {
		t.Fatalf("favorites before marking = %d, want 0", len(got.Items))
	}

	// Marking twice is fine.
	for range 2 {
		if rec := f.do(http.MethodPut, path, nil, reader); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT favorite status = %d body %s", rec.Code, rec.Body.String())
		}
	}
	got := favorites(reader)
	if len(got.Items) != 1 || got.Items[0].ID != doc.ID || !got.Items[0].Favorite {
		t.Fatalf("reader favorites = %+v", got.Items)
	}

	// The flag is personal: another user sees an unmarked document and an empty list.
	if got := favorites(admin); len(got.Items) != 0 {
		t.Fatalf("admin favorites = %+v, want none", got.Items)
	}
	var detail struct {
		Favorite bool `json:"favorite"`
	}
	decodeInto(t, f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents/%d", doc.ID), nil, admin), &detail)
	if detail.Favorite {
		t.Fatal("admin sees the reader's favorite")
	}
	decodeInto(t, f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents/%d", doc.ID), nil, reader), &detail)
	if !detail.Favorite {
		t.Fatal("reader does not see their own favorite on the document")
	}

	// Editing the document keeps the flag in the response.
	decodeInto(t, f.do(http.MethodPatch, fmt.Sprintf("/api/v1/documents/%d", doc.ID), map[string]any{"title": "Lease 2026"}, reader), &detail)
	if !detail.Favorite {
		t.Fatal("PATCH response lost the favorite flag")
	}

	if rec := f.do(http.MethodDelete, path, nil, reader); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE favorite status = %d", rec.Code)
	}
	if got := favorites(reader); len(got.Items) != 0 {
		t.Fatalf("favorites after removing = %+v", got.Items)
	}

	if rec := f.do(http.MethodPut, "/api/v1/documents/999999/favorite", nil, reader); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown document status = %d, want 404", rec.Code)
	}
}
