package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/archivr/backend/internal/archive"
)

type folderJSON struct {
	ID               int64  `json:"id"`
	ParentID         *int64 `json:"parent_id"`
	Path             string `json:"path"`
	Private          bool   `json:"private"`
	PrivateInherited bool   `json:"private_inherited"`
	Documents        int    `json:"document_count"`
	Children         int    `json:"child_count"`
}

func (f *fixture) newFolder(c *http.Cookie, name string, parent int64) folderJSON {
	f.t.Helper()
	body := map[string]any{"name": name}
	if parent != 0 {
		body["parent_id"] = parent
	}
	rec := f.do(http.MethodPost, "/api/v1/folders", body, c)
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("create folder %q: %d %s", name, rec.Code, rec.Body.String())
	}
	var out folderJSON
	decodeInto(f.t, rec, &out)
	return out
}

func (f *fixture) setPrivate(c *http.Cookie, id int64, private bool) *httptest.ResponseRecorder {
	return f.do(http.MethodPatch, fmt.Sprintf("/api/v1/folders/%d", id), map[string]any{"private": private}, c)
}

func (f *fixture) folders(c *http.Cookie) map[string]folderJSON {
	f.t.Helper()
	var list struct {
		Items []folderJSON `json:"items"`
	}
	rec := f.do(http.MethodGet, "/api/v1/folders", nil, c)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("list folders: %d", rec.Code)
	}
	decodeInto(f.t, rec, &list)
	out := map[string]folderJSON{}
	for _, it := range list.Items {
		out[it.Path] = it
	}
	return out
}

func (f *fixture) docIDs(c *http.Cookie, query string) []int64 {
	f.t.Helper()
	var list struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		Total int `json:"total"`
	}
	rec := f.do(http.MethodGet, "/api/v1/documents"+query, nil, c)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("list documents%s: %d %s", query, rec.Code, rec.Body.String())
	}
	decodeInto(f.t, rec, &list)
	ids := []int64{}
	for _, it := range list.Items {
		ids = append(ids, it.ID)
	}
	if list.Total != len(ids) {
		f.t.Fatalf("list documents%s: total %d for %d items", query, list.Total, len(ids))
	}
	return ids
}

// uploadInto uploads a PDF as the user and files it into a folder.
func (f *fixture) uploadInto(c *http.Cookie, folder int64, label string, extra map[string]any) int64 {
	f.t.Helper()
	rec := f.upload(c, label+".pdf", pdfData(label), label)
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("upload %q: %d %s", label, rec.Code, rec.Body.String())
	}
	var doc struct {
		ID int64 `json:"id"`
	}
	decodeInto(f.t, rec, &doc)
	body := map[string]any{"folder_id": folder}
	for k, v := range extra {
		body[k] = v
	}
	if rec := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/documents/%d", doc.ID), body, c); rec.Code != http.StatusOK {
		f.t.Fatalf("file %q: %d %s", label, rec.Code, rec.Body.String())
	}
	return doc.ID
}

func TestPrivateFolderIsShownToItsOwnerOnly(t *testing.T) {
	f := newFixture(t)
	owner, other := readerCookie(t, f), adminCookie(t, f)

	secret := f.newFolder(owner, "Secret", 0)
	shared := f.newFolder(owner, "Shared", 0)
	if secret.Private {
		t.Fatal("a new folder is shared")
	}

	rec := f.setPrivate(owner, secret.ID, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("make private: %d %s", rec.Code, rec.Body.String())
	}
	var got folderJSON
	decodeInto(t, rec, &got)
	if !got.Private {
		t.Fatalf("patched folder = %+v, want private", got)
	}

	if _, ok := f.folders(owner)["Secret"]; !ok {
		t.Fatal("the owner no longer sees the folder")
	}
	if !f.folders(owner)["Secret"].Private || f.folders(owner)["Shared"].Private {
		t.Fatalf("private flags = %+v", f.folders(owner))
	}
	seen := f.folders(other)
	if _, ok := seen["Secret"]; ok {
		t.Fatal("another user sees the private folder")
	}
	if _, ok := seen["Shared"]; !ok {
		t.Fatal("another user lost the shared folder")
	}

	// The folder cannot be reached by id either: it is not found, as if it did not exist.
	id := fmt.Sprintf("/api/v1/folders/%d", secret.ID)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"rename":  f.do(http.MethodPatch, id, map[string]any{"name": "Mine"}, other),
		"move":    f.do(http.MethodPatch, id, map[string]any{"parent_id": shared.ID}, other),
		"public":  f.setPrivate(other, secret.ID, false),
		"delete":  f.do(http.MethodDelete, id, nil, other),
		"subpath": f.do(http.MethodPost, "/api/v1/folders", map[string]any{"name": "Sub", "parent_id": secret.ID}, other),
	} {
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 404 or 400", name, rec.Code)
		}
	}
	if !f.folders(owner)["Secret"].Private {
		t.Fatal("another user changed the folder")
	}

	// Only the owner can share it again.
	if rec := f.setPrivate(owner, secret.ID, false); rec.Code != http.StatusOK {
		t.Fatalf("share again: %d", rec.Code)
	}
	if _, ok := f.folders(other)["Secret"]; !ok {
		t.Fatal("the folder stays hidden after it is shared")
	}
}

func TestPrivateFolderHidesItsDocuments(t *testing.T) {
	f := newFixture(t)
	owner, other := readerCookie(t, f), adminCookie(t, f)

	secret := f.newFolder(owner, "Secret", 0)
	open := f.newFolder(owner, "Open", 0)
	hidden := f.uploadInto(owner, secret.ID, "hidden-lease", map[string]any{
		"tags": []string{"landlord-only"}, "correspondent": "Private Landlord",
	})
	visible := f.uploadInto(owner, open.ID, "visible-lease", map[string]any{"tags": []string{"landlord-only", "common"}})
	if rec := f.setPrivate(owner, secret.ID, true); rec.Code != http.StatusOK {
		t.Fatalf("make private: %d", rec.Code)
	}

	// Lists and search.
	for _, q := range []string{"", "?q=lease", "?folder=" + fmt.Sprint(secret.ID), "?folder=" + fmt.Sprint(secret.ID) + "&subtree=1", "?tag=landlord-only"} {
		for _, id := range f.docIDs(other, q) {
			if id == hidden {
				t.Errorf("GET /documents%s lists a private document to another user", q)
			}
		}
	}
	if ids := f.docIDs(owner, "?q=lease"); len(ids) != 2 {
		t.Fatalf("the owner finds %v, want both documents", ids)
	}
	if ids := f.docIDs(other, "?q=lease"); len(ids) != 1 || ids[0] != visible {
		t.Fatalf("another user finds %v, want only %d", ids, visible)
	}

	// Every endpoint that takes a document number.
	doc := fmt.Sprintf("/api/v1/documents/%d", hidden)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"get":         f.do(http.MethodGet, doc, nil, other),
		"patch":       f.do(http.MethodPatch, doc, map[string]any{"title": "Mine now"}, other),
		"move out":    f.do(http.MethodPatch, doc, map[string]any{"folder_id": open.ID}, other),
		"delete":      f.do(http.MethodDelete, doc, nil, other),
		"file":        f.do(http.MethodGet, doc+"/file", nil, other),
		"thumbnail":   f.do(http.MethodGet, doc+"/thumbnail", nil, other),
		"suggestions": f.do(http.MethodGet, doc+"/suggestions", nil, other),
		"reocr":       f.do(http.MethodPost, doc+"/reocr", nil, other),
		"favorite":    f.do(http.MethodPut, doc+"/favorite", nil, other),
		"share":       f.do(http.MethodPost, doc+"/share", map[string]any{"expires_in_seconds": 3600}, other),
	} {
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", name, rec.Code)
		}
	}
	// A file cannot be filed into a folder the user cannot see.
	mine := f.uploadInto(other, 1, "admin-doc", nil)
	if rec := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/documents/%d", mine), map[string]any{"folder_id": secret.ID}, other); rec.Code != http.StatusBadRequest {
		t.Errorf("file into a private folder: status %d, want 400", rec.Code)
	}

	// Replacing the file of a hidden document.
	var body bytes.Buffer
	req := httptest.NewRequest(http.MethodPut, doc+"/file", &body)
	req.AddCookie(other)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Errorf("replace file of a private document: status %d", rec.Code)
	}

	// The owner is not affected, and the document is untouched.
	if rec := f.do(http.MethodGet, doc, nil, owner); rec.Code != http.StatusOK {
		t.Fatalf("owner get: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, doc+"/file", nil, owner); rec.Code != http.StatusOK {
		t.Fatalf("owner file: %d", rec.Code)
	}

	// Counts leave out what the viewer cannot see, and so do the value lists.
	var tags struct {
		Items []struct {
			Name      string `json:"name"`
			Documents int    `json:"document_count"`
		} `json:"items"`
	}
	decodeInto(t, f.do(http.MethodGet, "/api/v1/tags", nil, other), &tags)
	for _, tg := range tags.Items {
		if tg.Name == "landlord-only" && tg.Documents != 1 {
			t.Errorf("tag count for another user = %d, want 1", tg.Documents)
		}
	}
	var corr struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	decodeInto(t, f.do(http.MethodGet, "/api/v1/correspondents", nil, other), &corr)
	for _, c := range corr.Items {
		if c.Name == "Private Landlord" {
			t.Error("a correspondent only a private document names is listed to another user")
		}
	}
	decodeInto(t, f.do(http.MethodGet, "/api/v1/correspondents", nil, owner), &corr)
	found := false
	for _, c := range corr.Items {
		found = found || c.Name == "Private Landlord"
	}
	if !found {
		t.Error("the owner does not see the correspondent")
	}
}

func TestTagsOnlyPrivateDocumentsCarryAreHidden(t *testing.T) {
	f := newFixture(t)
	owner, other := readerCookie(t, f), adminCookie(t, f)
	secret := f.newFolder(owner, "Secret", 0)
	f.uploadInto(owner, secret.ID, "diary", map[string]any{"tags": []string{"diary-tag"}})
	f.setPrivate(owner, secret.ID, true)

	list := func(c *http.Cookie) []int64 {
		var tags struct {
			Items []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"items"`
		}
		decodeInto(t, f.do(http.MethodGet, "/api/v1/tags", nil, c), &tags)
		var ids []int64
		for _, tg := range tags.Items {
			if tg.Name == "diary-tag" {
				ids = append(ids, tg.ID)
			}
		}
		return ids
	}
	if len(list(other)) != 0 {
		t.Fatal("a tag only a private document carries is listed to another user")
	}
	ids := list(owner)
	if len(ids) != 1 {
		t.Fatal("the owner does not see the tag")
	}
	path := fmt.Sprintf("/api/v1/tags/%d", ids[0])
	if rec := f.do(http.MethodPatch, path, map[string]any{"name": "renamed"}, other); rec.Code != http.StatusNotFound {
		t.Errorf("rename a hidden tag: %d, want 404", rec.Code)
	}
	if rec := f.do(http.MethodDelete, path, nil, other); rec.Code != http.StatusNotFound {
		t.Errorf("delete a hidden tag: %d, want 404", rec.Code)
	}
	if rec := f.do(http.MethodPatch, path, map[string]any{"name": "renamed"}, owner); rec.Code != http.StatusOK {
		t.Errorf("owner renames the tag: %d", rec.Code)
	}
}

func TestPrivacyReachesSubfoldersAndSurvivesMoves(t *testing.T) {
	f := newFixture(t)
	owner, other := readerCookie(t, f), adminCookie(t, f)

	top := f.newFolder(owner, "Top", 0)
	pub := f.newFolder(owner, "Public", 0)
	child := f.newFolder(owner, "Child", top.ID)
	f.setPrivate(owner, top.ID, true)

	// A subfolder made inside a private folder is private, and is hidden with it.
	inside := f.newFolder(owner, "Inside", top.ID)
	if !inside.Private {
		t.Fatalf("a subfolder of a private folder is shared: %+v", inside)
	}
	seen := f.folders(other)
	for _, p := range []string{"Top", "Top/Child", "Top/Inside"} {
		if _, ok := seen[p]; ok {
			t.Errorf("another user sees %s", p)
		}
	}
	mine := f.folders(owner)
	if !mine["Top/Child"].PrivateInherited || mine["Top/Child"].Private {
		t.Errorf("Top/Child = %+v, want inherited privacy only", mine["Top/Child"])
	}
	doc := f.uploadInto(owner, child.ID, "deep", nil)
	if ids := f.docIDs(other, ""); len(ids) != 0 {
		t.Fatalf("another user lists %v from a private tree", ids)
	}
	if rec := f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents/%d", doc), nil, other); rec.Code != http.StatusNotFound {
		t.Fatalf("get a document under a private folder: %d", rec.Code)
	}

	// A folder that leaves the private tree stays private until it is shared on purpose.
	rec := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/folders/%d", child.ID), map[string]any{"parent_id": 0}, owner)
	if rec.Code != http.StatusOK {
		t.Fatalf("move out: %d %s", rec.Code, rec.Body.String())
	}
	var moved folderJSON
	decodeInto(t, rec, &moved)
	if !moved.Private {
		t.Fatalf("moved folder = %+v, want it to stay private", moved)
	}
	if _, ok := f.folders(other)["Child"]; ok {
		t.Fatal("moving a folder out of a private folder made it public")
	}
	if ids := f.docIDs(other, ""); len(ids) != 0 {
		t.Fatalf("another user lists %v after the move", ids)
	}
	_ = pub
}

func TestPrivateConflictsWithAnotherUsersPrivateFolder(t *testing.T) {
	f := newFixture(t)
	owner, other := readerCookie(t, f), adminCookie(t, f)
	parent := f.newFolder(owner, "Parent", 0)
	kid := f.newFolder(other, "Kid", parent.ID)
	if rec := f.setPrivate(other, kid.ID, true); rec.Code != http.StatusOK {
		t.Fatalf("make kid private: %d", rec.Code)
	}
	// Making the parent private would hide the other user's own folder from them.
	if rec := f.setPrivate(owner, parent.ID, true); rec.Code == http.StatusOK {
		t.Fatal("made a folder private over another user's private folder")
	}
}

func TestPrivateDocumentStaysPrivateInTrash(t *testing.T) {
	f := newFixture(t)
	owner, other := readerCookie(t, f), adminCookie(t, f)
	secret := f.newFolder(owner, "Secret", 0)
	doc := f.uploadInto(owner, secret.ID, "will", nil)
	f.setPrivate(owner, secret.ID, true)

	trash, err := f.store.FolderByPath(context.Background(), archive.TrashName)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/documents/%d", doc)
	if rec := f.do(http.MethodPatch, path, map[string]any{"folder_id": trash.ID}, owner); rec.Code != http.StatusOK {
		t.Fatalf("trash it: %d %s", rec.Code, rec.Body.String())
	}
	inTrash := fmt.Sprintf("?folder=%d", trash.ID)
	if ids := f.docIDs(other, inTrash); len(ids) != 0 {
		t.Fatalf("another user sees %v in Trash", ids)
	}
	if rec := f.do(http.MethodGet, path, nil, other); rec.Code != http.StatusNotFound {
		t.Fatalf("another user opens a trashed private document: %d", rec.Code)
	}
	if ids := f.docIDs(owner, inTrash); len(ids) != 1 {
		t.Fatalf("the owner sees %v in Trash, want the document", ids)
	}
	if n := f.folders(other)["Trash"].Documents; n != 0 {
		t.Fatalf("Trash counts %d documents for another user", n)
	}
	if n := f.folders(owner)["Trash"].Documents; n != 1 {
		t.Fatalf("Trash counts %d documents for the owner", n)
	}

	// Emptying Trash as admin leaves the private document alone.
	if rec := f.do(http.MethodPost, "/api/v1/trash/empty", nil, other); rec.Code != http.StatusOK {
		t.Fatalf("empty trash: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, path, nil, owner); rec.Code != http.StatusOK {
		t.Fatalf("another user's empty-trash removed a private document: %d", rec.Code)
	}

	// Restoring it into a shared folder makes it visible again, on purpose.
	if rec := f.do(http.MethodPatch, path, map[string]any{"folder_id": 1}, owner); rec.Code != http.StatusOK {
		t.Fatalf("restore: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, path, nil, other); rec.Code != http.StatusOK {
		t.Fatalf("restored document is still hidden: %d", rec.Code)
	}
}

func TestExpiredPrivateDocumentStaysPrivate(t *testing.T) {
	f := newFixture(t)
	owner, other := readerCookie(t, f), adminCookie(t, f)
	secret := f.newFolder(owner, "Secret", 0)
	doc := f.uploadInto(owner, secret.ID, "passport", map[string]any{"valid_until": "2000-01-01"})
	f.setPrivate(owner, secret.ID, true)

	// The expiry sweep runs as the system, with no signed-in user.
	if n, err := f.archive.ExpireDocuments(context.Background()); err != nil || n != 1 {
		t.Fatalf("expiry moved %d, err %v", n, err)
	}
	if rec := f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents/%d", doc), nil, other); rec.Code != http.StatusNotFound {
		t.Fatalf("another user opens an expired private document: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, fmt.Sprintf("/api/v1/documents/%d", doc), nil, owner); rec.Code != http.StatusOK {
		t.Fatalf("owner opens it: %d", rec.Code)
	}
}

func TestDuplicateUploadDoesNotNameAPrivateDocument(t *testing.T) {
	f := newFixture(t)
	owner, other := readerCookie(t, f), adminCookie(t, f)
	secret := f.newFolder(owner, "Secret", 0)
	doc := f.uploadInto(owner, secret.ID, "same-bytes", nil)
	f.setPrivate(owner, secret.ID, true)

	rec := f.upload(other, "copy.pdf", pdfData("same-bytes"), "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), fmt.Sprint(doc)) {
		t.Fatalf("the message names a private document: %s", rec.Body.String())
	}
}

func TestOwnerCanShareAPrivateDocument(t *testing.T) {
	f := newFixture(t)
	owner := readerCookie(t, f)
	secret := f.newFolder(owner, "Secret", 0)
	doc := f.uploadInto(owner, secret.ID, "handout", nil)
	f.setPrivate(owner, secret.ID, true)

	rec := f.do(http.MethodPost, fmt.Sprintf("/api/v1/documents/%d/share", doc), map[string]any{"expires_in_seconds": 3600}, owner)
	if rec.Code != http.StatusCreated {
		t.Fatalf("share: %d %s", rec.Code, rec.Body.String())
	}
	var link struct {
		Token string `json:"token"`
	}
	decodeInto(t, rec, &link)
	if rec := f.do(http.MethodGet, "/api/v1/shares/"+link.Token+"/file", nil); rec.Code != http.StatusOK {
		t.Fatalf("open the link: %d", rec.Code)
	}
}

func TestUserWithPrivateFoldersCannotBeDeleted(t *testing.T) {
	f := newFixture(t)
	owner, admin := readerCookie(t, f), adminCookie(t, f)
	secret := f.newFolder(owner, "Secret", 0)
	f.setPrivate(owner, secret.ID, true)

	u, err := f.store.UserByEmail(context.Background(), readerEmail)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/users/%d", u.ID)
	if rec := f.do(http.MethodDelete, path, nil, admin); rec.Code != http.StatusConflict {
		t.Fatalf("delete: %d, want 409", rec.Code)
	}
	f.setPrivate(owner, secret.ID, false)
	if rec := f.do(http.MethodDelete, path, nil, admin); rec.Code != http.StatusNoContent {
		t.Fatalf("delete after sharing: %d", rec.Code)
	}
}

func TestSystemFoldersCannotBePrivate(t *testing.T) {
	f := newFixture(t)
	c := readerCookie(t, f)
	if rec := f.setPrivate(c, archive.InboxID, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("make INBOX private: %d", rec.Code)
	}
}
