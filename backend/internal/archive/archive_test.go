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

func TestReplaceFileKeepsNumberAndDetailsAndSwapsTheBytes(t *testing.T) {
	svc, st, layout := newService(t)
	ctx := context.Background()
	doc := create(t, svc, "Contract", "old", 0)
	other := create(t, svc, "Other", "other", 0)
	if err := st.SaveOCRText(ctx, doc.ID, "old words", "tesseract", "eng"); err != nil {
		t.Fatal(err)
	}
	rot := 90
	tags := []string{"keep"}
	if _, err := svc.Update(ctx, doc.ID, Patch{Rotation: &rot, Tags: &tags}); err != nil {
		t.Fatal(err)
	}

	png := []byte("\x89PNG\r\n\x1a\nnew")
	got, err := svc.ReplaceFile(ctx, doc.ID, png, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != doc.ID || got.Title != "Contract" || len(got.Tags) != 1 || got.Tags[0] != "keep" {
		t.Fatalf("details changed: %+v", got)
	}
	if got.Mime != "image/png" || got.Size != int64(len(png)) || got.SHA256 == doc.SHA256 {
		t.Fatalf("file fields not updated: %+v", got)
	}
	if got.Rotation != 0 || got.OCRStatus != "pending" {
		t.Fatalf("rotation = %d, ocr = %q", got.Rotation, got.OCRStatus)
	}
	if text, _ := st.DocumentText(ctx, doc.ID); text != "" {
		t.Fatalf("old text kept: %q", text)
	}
	if exists(t, mustFile(t, layout, "INBOX", doc.Filename)) {
		t.Fatal("old file still on disk")
	}
	if exists(t, mustFile(t, layout, "INBOX", doc.Filename)+".archivr.json") {
		t.Fatal("old sidecar still on disk")
	}
	newPath := mustFile(t, layout, "INBOX", got.Filename)
	if data, err := os.ReadFile(newPath); err != nil || string(data) != string(png) {
		t.Fatalf("new file = %q, %v", data, err)
	}
	if !exists(t, newPath+".archivr.json") {
		t.Fatal("sidecar missing for the new file")
	}
	if entries, _ := os.ReadDir(layout.TrashDir()); len(entries) != 0 {
		t.Fatalf("trash directory not cleaned: %v", entries)
	}

	// The same bytes, or bytes another document holds, are refused and change nothing.
	if _, err := svc.ReplaceFile(ctx, doc.ID, png, "image/png"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same file: err = %v, want ErrInvalid", err)
	}
	var dup *DuplicateError
	if _, err := svc.ReplaceFile(ctx, doc.ID, pdfBytes("other"), "application/pdf"); !errors.As(err, &dup) || dup.Document.ID != other.ID {
		t.Fatalf("duplicate: err = %v", err)
	}
	if !exists(t, newPath) {
		t.Fatal("a refused replace removed the current file")
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

type recordingQueue struct{ ids []int64 }

func (q *recordingQueue) Enqueue(id int64) { q.ids = append(q.ids, id) }

// Turning a document changes how OCR reads its pages, so the text is read again. Other
// edits do not queue OCR again.
func TestRotationChangeQueuesOCRAgain(t *testing.T) {
	svc, st, _ := newService(t)
	queue := &recordingQueue{}
	svc.OCR = queue
	ctx := context.Background()
	doc := create(t, svc, "Sideways scan", "sideways", 0)
	if err := st.SaveOCRText(ctx, doc.ID, "garbage", "tesseract", "eng"); err != nil {
		t.Fatal(err)
	}
	queue.ids = nil

	title := "Renamed"
	if _, err := svc.Update(ctx, doc.ID, Patch{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if len(queue.ids) != 0 {
		t.Fatalf("title edit queued OCR: %v", queue.ids)
	}

	ninety := 90
	got, err := svc.Update(ctx, doc.ID, Patch{Rotation: &ninety})
	if err != nil {
		t.Fatal(err)
	}
	if got.OCRStatus != "pending" {
		t.Fatalf("ocr = %q, want pending", got.OCRStatus)
	}
	if len(queue.ids) != 1 || queue.ids[0] != doc.ID {
		t.Fatalf("queued = %v, want [%d]", queue.ids, doc.ID)
	}

	// The same turn again changes nothing, so nothing is queued.
	queue.ids = nil
	if _, err := svc.Update(ctx, doc.ID, Patch{Rotation: &ninety}); err != nil {
		t.Fatal(err)
	}
	if len(queue.ids) != 0 {
		t.Fatalf("unchanged rotation queued OCR: %v", queue.ids)
	}
}

// Rotation is stored and survives other edits, and the file on disk is never touched.
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
		if err := tx.SaveOCRText(ctx, doc.ID, "Facture électricité, montant 88 euros, cœur", "tesseract", "fra"); err != nil {
			return err
		}
		return tx.SyncFTS(ctx, doc.ID)
	}); err != nil {
		t.Fatal(err)
	}

	// Accents and ligatures are folded on both sides, so the plain form finds
	// the French word and the accented form finds the plain one.
	for _, q := range []string{"electricite", "électricité", "coeur", "cœur"} {
		docs, total, err := st.ListDocuments(ctx, store.DocFilter{Query: q})
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		if total != 1 || docs[0].ID != doc.ID {
			t.Fatalf("query %q: total=%d", q, total)
		}
		if docs[0].Snippet == "" {
			t.Fatalf("query %q: expected a snippet", q)
		}
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

// prime creates the directories of the system folders (INBOX, Trash), so later reports
// only count what a test changed.
func prime(t *testing.T, svc *Service) {
	t.Helper()
	if _, err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// reconcileTwice runs Reconcile, then runs it again and checks the second run is a no-op.
func reconcileTwice(t *testing.T, svc *Service) ReconcileReport {
	t.Helper()
	ctx := context.Background()
	first, err := svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	second, err := svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if second.Changed() {
		t.Fatalf("second reconcile changed things: %+v", second)
	}
	return first
}

// A crash after the file moved but before the commit leaves the database pointing at
// the old path. Reconcile moves the file, and its sidecar, back.
func TestReconcileRepairsInterruptedDocumentMove(t *testing.T) {
	svc, _, layout := newService(t)
	prime(t, svc)
	ctx := context.Background()
	doc := create(t, svc, "Scan", "rc-move", 0)
	if _, err := svc.CreateFolder(ctx, nil, "Finance"); err != nil {
		t.Fatal(err)
	}

	dbPath := mustFile(t, layout, "INBOX", doc.Filename)
	strayName := "10000 - Renamed by a crashed update.pdf"
	stray := mustFile(t, layout, "Finance", strayName)
	if err := storage.MoveFile(dbPath, stray); err != nil {
		t.Fatal(err)
	}

	rep := reconcileTwice(t, svc)
	if rep.MovedFiles != 1 || len(rep.Problems) != 0 {
		t.Fatalf("report = %+v, want one moved file and no problems", rep)
	}
	if !exists(t, dbPath) || !exists(t, dbPath+".archivr.json") {
		t.Fatal("file and sidecar were not moved back to the path in the database")
	}
	if exists(t, stray) || exists(t, stray+".archivr.json") {
		t.Fatal("stray file or sidecar left behind")
	}
}

// A crash during a folder move leaves the directory under its new name while the rows
// still carry the old path. Reconcile moves the files back and clears the empty husk.
func TestReconcileRepairsInterruptedFolderMove(t *testing.T) {
	svc, _, layout := newService(t)
	prime(t, svc)
	ctx := context.Background()
	home, err := svc.CreateFolder(ctx, nil, "Home")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := svc.CreateFolder(ctx, &home.ID, "Lease")
	if err != nil {
		t.Fatal(err)
	}
	doc := create(t, svc, "Contract", "rc-folder", lease.ID)

	oldDir, _ := layout.FolderDir("Home")
	newDir, _ := layout.FolderDir("House")
	if err := storage.MoveFolder(oldDir, newDir); err != nil {
		t.Fatal(err)
	}

	rep := reconcileTwice(t, svc)
	if rep.MovedFiles != 1 || len(rep.Problems) != 0 {
		t.Fatalf("report = %+v, want one moved file and no problems", rep)
	}
	if rep.CreatedFolders != 2 || rep.RemovedDirs != 2 {
		t.Fatalf("report = %+v, want 2 folders created and 2 stray dirs removed", rep)
	}
	file := mustFile(t, layout, "Home/Lease", doc.Filename)
	if !exists(t, file) || !exists(t, file+".archivr.json") {
		t.Fatal("document did not return to the folder the database expects")
	}
	if exists(t, newDir) {
		t.Fatal("stray directory of the interrupted move still exists")
	}
}

// A crash during a delete can leave the file in trash/ with its row still there, or
// with the row gone. The first is restored, the second is removed.
func TestReconcileRepairsInterruptedDelete(t *testing.T) {
	svc, _, layout := newService(t)
	prime(t, svc)
	doc := create(t, svc, "Receipt", "rc-delete-live", 0)
	live := mustFile(t, layout, "INBOX", doc.Filename)

	trashedLive := filepath.Join(layout.TrashDir(), "10000-111-"+doc.Filename)
	if err := storage.MoveFile(live, trashedLive); err != nil {
		t.Fatal(err)
	}
	// An entry whose row was deleted: the commit succeeded but the trash was not emptied.
	gone := filepath.Join(layout.TrashDir(), "10099-222-10099 - Gone.pdf")
	if err := os.MkdirAll(layout.TrashDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gone, pdfBytes("gone"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storage.SidecarPath(gone), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := reconcileTwice(t, svc)
	if rep.RestoredTrash != 1 || rep.RemovedTrash != 1 || len(rep.Problems) != 0 {
		t.Fatalf("report = %+v, want one restored and one removed", rep)
	}
	if !exists(t, live) || !exists(t, live+".archivr.json") {
		t.Fatal("trashed file with a live row was not restored")
	}
	if exists(t, trashedLive) || exists(t, gone) || exists(t, storage.SidecarPath(gone)) {
		t.Fatal("trash entries left behind")
	}
}

func TestReconcileCreatesMissingFolderDirectory(t *testing.T) {
	svc, _, layout := newService(t)
	prime(t, svc)
	f, err := svc.CreateFolder(context.Background(), nil, "Tax")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := layout.FolderDir(f.Path)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	rep := reconcileTwice(t, svc)
	if rep.CreatedFolders != 1 || !exists(t, dir) {
		t.Fatalf("report = %+v, directory exists = %v", rep, exists(t, dir))
	}
}

// What Reconcile cannot fix, it reports and leaves alone.
func TestReconcileReportsWhatItCannotFix(t *testing.T) {
	svc, st, layout := newService(t)
	prime(t, svc)
	ctx := context.Background()
	missing := create(t, svc, "Vanished", "rc-missing", 0)
	clash := create(t, svc, "Clash", "rc-clash", 0)
	if _, err := svc.CreateFolder(ctx, nil, "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateFolder(ctx, nil, "B"); err != nil {
		t.Fatal(err)
	}

	// One document's file is gone altogether.
	missingFile := mustFile(t, layout, "INBOX", missing.Filename)
	if err := os.Remove(missingFile); err != nil {
		t.Fatal(err)
	}
	// Another has vanished from its place, and two files claim its ID.
	clashFile := mustFile(t, layout, "INBOX", clash.Filename)
	if err := os.Remove(clashFile); err != nil {
		t.Fatal(err)
	}
	a := mustFile(t, layout, "A", clash.Filename)
	b := mustFile(t, layout, "B", clash.Filename)
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, pdfBytes("rc-clash"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// And a file in the tree that no row owns.
	orphan := mustFile(t, layout, "A", "20000 - Orphan.pdf")
	if err := os.WriteFile(orphan, pdfBytes("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := reconcileTwice(t, svc)
	if rep.Changed() {
		t.Fatalf("report = %+v, want no changes", rep)
	}
	if len(rep.Problems) != 3 {
		t.Fatalf("problems = %q, want 3", rep.Problems)
	}
	for _, p := range []string{a, b, orphan} {
		if !exists(t, p) {
			t.Fatalf("%s was touched", p)
		}
	}
	if _, err := st.GetDocument(ctx, missing.ID); err != nil {
		t.Fatalf("reconcile changed the database: %v", err)
	}
}

// A normal archive needs no repair.
func TestReconcileLeavesAHealthyArchiveAlone(t *testing.T) {
	svc, _, _ := newService(t)
	prime(t, svc)
	ctx := context.Background()
	f, _ := svc.CreateFolder(ctx, nil, "Tax")
	create(t, svc, "One", "rc-h1", 0)
	create(t, svc, "Two", "rc-h2", f.ID)
	if rep := reconcileTwice(t, svc); rep.Changed() || len(rep.Problems) != 0 {
		t.Fatalf("report = %+v, want nothing", rep)
	}
}
