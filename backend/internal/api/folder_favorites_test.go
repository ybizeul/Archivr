package api

import (
	"fmt"
	"net/http"
	"testing"
)

func TestFolderFavorites(t *testing.T) {
	f := newFixture(t)
	reader := readerCookie(t, f)
	admin := adminCookie(t, f)

	lease := f.newFolder(reader, "Lease", 0)
	path := fmt.Sprintf("/api/v1/folders/%d/favorite", lease.ID)

	favoriteOf := func(c *http.Cookie, id int64) bool {
		t.Helper()
		var list struct {
			Items []struct {
				ID       int64 `json:"id"`
				Favorite bool  `json:"favorite"`
			} `json:"items"`
		}
		decodeInto(t, f.do(http.MethodGet, "/api/v1/folders", nil, c), &list)
		for _, it := range list.Items {
			if it.ID == id {
				return it.Favorite
			}
		}
		t.Fatalf("folder %d not listed", id)
		return false
	}

	if favoriteOf(reader, lease.ID) {
		t.Fatal("folder is a favorite before it was marked")
	}

	// Marking twice is fine.
	for range 2 {
		if rec := f.do(http.MethodPut, path, nil, reader); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT folder favorite status = %d body %s", rec.Code, rec.Body.String())
		}
	}
	if !favoriteOf(reader, lease.ID) {
		t.Fatal("reader does not see their own folder favorite")
	}

	// The flag is personal: another user sees the same shared folder, unmarked.
	if favoriteOf(admin, lease.ID) {
		t.Fatal("admin sees the reader's folder favorite")
	}

	// Editing the folder keeps the flag in the response.
	var patched struct {
		Favorite bool `json:"favorite"`
	}
	rec := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/folders/%d", lease.ID), map[string]any{"name": "Lease 2026"}, reader)
	decodeInto(t, rec, &patched)
	if !patched.Favorite {
		t.Fatal("PATCH response lost the folder favorite flag")
	}

	if rec := f.do(http.MethodDelete, path, nil, reader); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE folder favorite status = %d", rec.Code)
	}
	if favoriteOf(reader, lease.ID) {
		t.Fatal("folder is still a favorite after removing it")
	}

	if rec := f.do(http.MethodPut, "/api/v1/folders/999999/favorite", nil, reader); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown folder status = %d, want 404", rec.Code)
	}
}

func TestPrivateFolderCannotBeFavoritedByOthers(t *testing.T) {
	f := newFixture(t)
	reader := readerCookie(t, f)
	admin := adminCookie(t, f)

	secret := f.newFolder(admin, "Secret", 0)
	if rec := f.setPrivate(admin, secret.ID, true); rec.Code != http.StatusOK {
		t.Fatalf("make private: %d %s", rec.Code, rec.Body.String())
	}
	path := fmt.Sprintf("/api/v1/folders/%d/favorite", secret.ID)

	// Another user, admins included, cannot see the folder, so it is not found.
	if rec := f.do(http.MethodPut, path, nil, reader); rec.Code != http.StatusNotFound {
		t.Fatalf("reader favorites a private folder: status %d, want 404", rec.Code)
	}

	// The owner can.
	if rec := f.do(http.MethodPut, path, nil, admin); rec.Code != http.StatusNoContent {
		t.Fatalf("owner favorites own private folder: status %d", rec.Code)
	}
}
