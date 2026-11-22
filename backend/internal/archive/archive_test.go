package archive

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/archivr/backend/internal/db"
	"github.com/archivr/backend/internal/storage"
	"github.com/archivr/backend/internal/store"
)

func newService(t *testing.T) (*Service, *store.Store, storage.Layout) {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(context.Background(), filepath.Join(dir, "archivr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	st := store.New(conn)
	layout := storage.Layout{DataDir: dir}
	svc := &Service{
		Store:  st,
		Layout: layout,
		Now:    time.Now,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return svc, st, layout
}

// pdfBytes returns distinct content for each label, so every test document is unique.
func pdfBytes(label string) []byte {
	return []byte("%PDF-1.4\n% " + label + "\n%%EOF\n")
}

func create(t *testing.T, svc *Service, title, label string, folderID int64) store.Document {
	t.Helper()
	doc, err := svc.Create(context.Background(), NewDocument{
		Title:    title,
		FolderID: folderID,
		Data:     pdfBytes(label),
		Mime:     "application/pdf",
		Source:   "upload",
	})
	if err != nil {
		t.Fatalf("create %q: %v", title, err)
	}
	return doc
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func mustFile(t *testing.T, layout storage.Layout, folderPath, name string) string {
	t.Helper()
	p, err := layout.DocumentFile(folderPath, name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFirstDocumentIsNumber10000AndFiledInInbox(t *testing.T) {
	svc, _, layout := newService(t)
	doc := create(t, svc, "Landlord letter", "a", 0)

	if doc.ID != 10000 {
		t.Fatalf("id = %d, want 10000", doc.ID)
	}
	if doc.Folder.Path != "INBOX" || doc.FolderID != InboxID {
		t.Fatalf("folder = %+v, want INBOX", doc.Folder)
	}
	if doc.Filename != "10000 - Landlord letter.pdf" {
		t.Fatalf("filename = %q", doc.Filename)
	}
	if !exists(t, mustFile(t, layout, "INBOX", doc.Filename)) {
		t.Fatal("document file was not written to INBOX")
	}
	if !exists(t, mustFile(t, layout, "INBOX", doc.Filename)+".archivr.json") {
		t.Fatal("sidecar was not written")
	}
}

func TestSameBytesAreRejectedAsDuplicate(t *testing.T) {
	svc, _, _ := newService(t)
	first := create(t, svc, "Invoice", "same", 0)

	_, err := svc.Create(context.Background(), NewDocument{
		Title: "Invoice again", Data: pdfBytes("same"), Mime: "application/pdf", Source: "upload",
	})
	var dup *DuplicateError
	if !errors.As(err, &dup) {
		t.Fatalf("err = %v, want DuplicateError", err)
	}
	if dup.Document.ID != first.ID {
		t.Fatalf("duplicate of %d, want %d", dup.Document.ID, first.ID)
	}
}

func TestUpdateRenamesAndMovesFileWithSidecar(t *testing.T) {
	svc, st, layout := newService(t)
	ctx := context.Background()
	doc := create(t, svc, "Scan", "b", 0)

	finance, err := svc.CreateFolder(ctx, nil, "Finance")
	if err != nil {
		t.Fatal(err)
	}
	title := "Electric bill"
	moved, err := svc.Update(ctx, doc.ID, Patch{Title: &title, FolderID: &finance.ID})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Folder.Path != "Finance" || moved.Title != title {
		t.Fatalf("updated = %+v", moved)
	}
	oldFile := mustFile(t, layout, "INBOX", doc.Filename)
	newFile := mustFile(t, layout, "Finance", "10000 - Electric bill.pdf")
	if exists(t, oldFile) || !exists(t, newFile) {
		t.Fatalf("file not moved: old=%v new=%v", exists(t, oldFile), exists(t, newFile))
	}
	if !exists(t, newFile+".archivr.json") {
		t.Fatal("sidecar did not follow the file")
	}
	if exists(t, oldFile+".archivr.json") {
		t.Fatal("old sidecar left behind")
	}

	// The new title is searchable at once.
	docs, total, err := st.ListDocuments(ctx, store.DocFilter{Query: "electric"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || docs[0].ID != doc.ID {
		t.Fatalf("search for new title: total=%d docs=%+v", total, docs)
	}
}

// Rotation is display metadata: it is stored and survives other edits, and the file on
// disk is never touched.
func TestRotationIsMetadataOnly(t *testing.T) {
	svc, _, layout := newService(t)
	ctx := context.Background()
	doc := create(t, svc, "Scan", "rot", 0)
	file := mustFile(t, layout, "INBOX", doc.Filename)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Rotation != 0 {
		t.Fatalf("new document rotation = %d, want 0", doc.Rotation)
	}

	ninety := 90
	got, err := svc.Update(ctx, doc.ID, Patch{Rotation: &ninety})
	if err != nil {
		t.Fatal(err)
	}
	if got.Rotation != 90 {
		t.Fatalf("rotation = %d, want 90", got.Rotation)
	}
	title := "Renamed"
	got, err = svc.Update(ctx, doc.ID, Patch{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	if got.Rotation != 90 {
		t.Fatalf("rotation after another edit = %d, want 90", got.Rotation)
	}

	bad := 45
	if _, err := svc.Update(ctx, doc.ID, Patch{Rotation: &bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rotation 45: err = %v, want ErrInvalid", err)
	}

	after, err := os.ReadFile(mustFile(t, layout, "INBOX", got.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("the file on disk changed")
	}
}

func TestFailedDatabaseStepRollsBackTheFileMove(t *testing.T) {
	svc, st, layout := newService(t)
	ctx := context.Background()
	doc := create(t, svc, "Original", "c", 0)

	// Force the database step to fail after the file has already moved.
	if _, err := st.DB.Exec(`
		CREATE TRIGGER refuse_boom BEFORE UPDATE ON documents
		WHEN NEW.title = 'boom'
		BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatal(err)
	}

	boom := "boom"
	if _, err := svc.Update(ctx, doc.ID, Patch{Title: &boom}); err == nil {
		t.Fatal("update succeeded, want forced failure")
	}

	original := mustFile(t, layout, "INBOX", doc.Filename)
	if !exists(t, original) {
		t.Fatal("file was not moved back after the database failure")
	}
	after, err := st.GetDocument(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != "Original" || after.Filename != doc.Filename {
		t.Fatalf("database changed despite rollback: %+v", after)
	}
}

func TestDeleteRemovesFileRowAndSearchEntry(t *testing.T) {
	svc, st, layout := newService(t)
	ctx := context.Background()
	doc := create(t, svc, "Receipt", "d", 0)
	if err := st.SaveOCRText(ctx, doc.ID, "Total due 42 euros", "tesseract", "eng"); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncFTS(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}

	if err := svc.Delete(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}
	if exists(t, mustFile(t, layout, "INBOX", doc.Filename)) {
		t.Fatal("file still present after delete")
	}
	if _, err := st.GetDocument(ctx, doc.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row still present: %v", err)
	}
	if _, total, _ := st.ListDocuments(ctx, store.DocFilter{Query: "euros"}); total != 0 {
		t.Fatalf("search still finds deleted document: %d", total)
	}
}

func TestFolderRenameKeepsFilesAndSubtree(t *testing.T) {
	svc, st, layout := newService(t)
	ctx := context.Background()
	home, err := svc.CreateFolder(ctx, nil, "Home")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := svc.CreateFolder(ctx, &home.ID, "Lease")
	if err != nil {
		t.Fatal(err)
	}
	doc := create(t, svc, "Contract", "e", lease.ID)

	name := "House"
	if _, err := svc.UpdateFolder(ctx, home.ID, FolderPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if !exists(t, mustFile(t, layout, "House/Lease", doc.Filename)) {
		t.Fatal("document did not move with its folder")
	}
	got, err := st.GetDocument(ctx, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Folder.Path != "House/Lease" {
		t.Fatalf("folder path = %q, want House/Lease", got.Folder.Path)
	}
	if _, err := os.Stat(filepath.Join(layout.DocumentsRoot(), "Home")); !os.IsNotExist(err) {
		t.Fatal("old folder directory still exists")
	}
}

func TestFolderMoveIntoItselfIsRefused(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	home, _ := svc.CreateFolder(ctx, nil, "Home")
	sub, _ := svc.CreateFolder(ctx, &home.ID, "Sub")
	_, err := svc.UpdateFolder(ctx, home.ID, FolderPatch{ParentID: &sub.ID})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestInboxCannotBeRenamedOrDeleted(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	name := "Post"
	if _, err := svc.UpdateFolder(ctx, InboxID, FolderPatch{Name: &name}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rename INBOX: %v", err)
	}
	if err := svc.DeleteFolder(ctx, InboxID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("delete INBOX: %v", err)
	}
}

func TestNonEmptyFolderCannotBeDeleted(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	f, _ := svc.CreateFolder(ctx, nil, "Tax")
	create(t, svc, "Return", "f", f.ID)
	if err := svc.DeleteFolder(ctx, f.ID); !errors.Is(err, store.ErrInUse) {
		t.Fatalf("err = %v, want ErrInUse", err)
	}
}

func TestFolderNamesCannotEscapeTheTree(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	for _, bad := range []string{"..", "../etc", "a/b", ".hidden", "", "con:x"} {
		if _, err := svc.CreateFolder(ctx, nil, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("CreateFolder(%q) err = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestFolderNamesAreCaseInsensitivelyUnique(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.CreateFolder(ctx, nil, "Finance"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateFolder(ctx, nil, "finance"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestOCRTextIsSearchableAndQueriesAreSafe(t *testing.T) {
	svc, st, _ := newService(t)
	ctx := context.Background()
	doc := create(t, svc, "Bill", "g", 0)
	if err := st.InTx(ctx, func(tx *store.Store) error {
		if err := tx.SaveOCRText(ctx, doc.ID, "Facture électricité, montant 88 euros", "tesseract", "fra"); err != nil {
			return err
		}
		return tx.SyncFTS(ctx, doc.ID)
	}); err != nil {
		t.Fatal(err)
	}

	// Accents are removed on both sides, so the plain form finds the French word.
	docs, total, err := st.ListDocuments(ctx, store.DocFilter{Query: "electricite"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || docs[0].ID != doc.ID {
		t.Fatalf("electricite: total=%d", total)
	}
	if docs[0].Snippet == "" {
		t.Fatal("expected a snippet from the OCR text")
	}

	// FTS operators in user input are treated as words, never as syntax.
	if _, _, err := st.ListDocuments(ctx, store.DocFilter{Query: `bill") OR (x NEAR`}); err != nil {
		t.Fatalf("hostile query failed: %v", err)
	}
}

func TestTagRenameRefreshesSearch(t *testing.T) {
	svc, st, _ := newService(t)
	ctx := context.Background()
	doc := create(t, svc, "Statement", "h", 0)
	tags := []string{"bank"}
	if _, err := svc.Update(ctx, doc.ID, Patch{Tags: &tags}); err != nil {
		t.Fatal(err)
	}
	tagID := int64(0)
	list, _ := st.ListTags(ctx, "bank")
	if len(list) != 1 {
		t.Fatalf("tags = %+v", list)
	}
	tagID = list[0].ID
	if _, err := svc.RenameTag(ctx, tagID, "finance"); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := st.ListDocuments(ctx, store.DocFilter{Query: "finance"}); total != 1 {
		t.Fatalf("renamed tag not searchable: %d", total)
	}
	if _, total, _ := st.ListDocuments(ctx, store.DocFilter{Query: "bank"}); total != 0 {
		t.Fatalf("old tag name still searchable: %d", total)
	}
}

func TestValidDate(t *testing.T) {
	for in, want := range map[string]bool{
		"2026-10-14": true, "2026-02-30": false, "14/10/2026": false, "": false,
	} {
		if got := ValidDate(in); got != want {
			t.Errorf("ValidDate(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestExpiredDocumentsMoveToTrash(t *testing.T) {
	svc, st, layout := newService(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 9, 0, 0, 0, time.Local)
	svc.Now = func() time.Time { return now }

	mk := func(label string, validUntil *string) store.Document {
		doc, err := svc.Create(ctx, NewDocument{
			Title: label, Data: pdfBytes(label), Mime: "application/pdf", Source: "upload", ValidUntil: validUntil,
		})
		if err != nil {
			t.Fatalf("create %q: %v", label, err)
		}
		return doc
	}
	past, today, future := "2026-11-21", "2026-11-22", "2027-01-01"
	expired := mk("expired", &past)
	lastDay := mk("last day", &today)
	later := mk("later", &future)
	forever := mk("forever", nil)

	if expired.ValidUntil == nil || *expired.ValidUntil != past {
		t.Fatalf("valid_until not stored: %v", expired.ValidUntil)
	}
	if _, err := svc.Create(ctx, NewDocument{Title: "bad", Data: pdfBytes("bad"), Mime: "application/pdf",
		Source: "upload", ValidUntil: strPtr("tomorrow")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad date error = %v, want ErrInvalid", err)
	}

	n, err := svc.ExpireDocuments(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ExpireDocuments = %d, %v; want 1, nil", n, err)
	}
	trash, err := st.FolderByPath(ctx, TrashName)
	if err != nil || !trash.IsSystem {
		t.Fatalf("trash folder = %+v, %v", trash, err)
	}
	got, _ := st.GetDocument(ctx, expired.ID)
	if got.FolderID != trash.ID {
		t.Fatalf("expired document is in folder %d, want trash %d", got.FolderID, trash.ID)
	}
	if !exists(t, mustFile(t, layout, "Trash", got.Filename)) {
		t.Fatal("file did not move into the Trash folder")
	}
	for _, d := range []store.Document{lastDay, later, forever} {
		cur, _ := st.GetDocument(ctx, d.ID)
		if cur.FolderID != InboxID {
			t.Errorf("%q moved to folder %d but has not expired", d.Title, cur.FolderID)
		}
	}
	if n, _ := svc.ExpireDocuments(ctx); n != 0 {
		t.Fatalf("second sweep moved %d documents, want 0", n)
	}

	// The day after the last valid day, it goes too.
	now = now.AddDate(0, 0, 1)
	if n, _ := svc.ExpireDocuments(ctx); n != 1 {
		t.Fatalf("next-day sweep moved %d, want 1", n)
	}

	// Restoring an expired document clears its date, so it is not trashed again.
	home := int64(InboxID)
	restored, err := svc.Update(ctx, expired.ID, Patch{FolderID: &home})
	if err != nil {
		t.Fatal(err)
	}
	if restored.ValidUntil != nil {
		t.Fatalf("valid_until after restore = %v, want cleared", *restored.ValidUntil)
	}
	if n, _ := svc.ExpireDocuments(ctx); n != 0 {
		t.Fatalf("sweep re-trashed a restored document (%d)", n)
	}

	// The date can be changed and cleared.
	set, err := svc.Update(ctx, later.ID, Patch{ValidUntil: strPtr("2030-05-06")})
	if err != nil || set.ValidUntil == nil || *set.ValidUntil != "2030-05-06" {
		t.Fatalf("set valid_until = %v, %v", set.ValidUntil, err)
	}
	cleared, err := svc.Update(ctx, later.ID, Patch{ValidUntil: strPtr("")})
	if err != nil || cleared.ValidUntil != nil {
		t.Fatalf("clear valid_until = %v, %v", cleared.ValidUntil, err)
	}
	if _, err := svc.Update(ctx, later.ID, Patch{ValidUntil: strPtr("soon")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad date error = %v, want ErrInvalid", err)
	}
}

func strPtr(s string) *string { return &s }
