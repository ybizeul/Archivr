package ingest

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/db"
	"github.com/archivr/backend/internal/storage"
	"github.com/archivr/backend/internal/store"
)

type fixture struct {
	st       *store.Store
	layout   storage.Layout
	importer *Importer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(context.Background(), filepath.Join(dir, "archivr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	st := store.New(conn)
	layout := storage.Layout{DataDir: dir}
	svc := &archive.Service{
		Store:  st,
		Layout: layout,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return fixture{st: st, layout: layout, importer: &Importer{Archive: svc, Store: st}}
}

func pdf(label string) []byte {
	return []byte("%PDF-1.4\n% " + label + "\n%%EOF\n")
}

func inboxCount(t *testing.T, st *store.Store) int {
	t.Helper()
	_, total, err := st.ListDocuments(context.Background(), store.DocFilter{FolderID: archive.InboxID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

func TestSniffMIMEReadsContentNotNames(t *testing.T) {
	cases := map[string][]byte{
		"application/pdf": []byte("%PDF-1.7 rest"),
		"image/png":       []byte("\x89PNG\r\n\x1a\n...."),
		"image/jpeg":      {0xFF, 0xD8, 0xFF, 0xE0, 0, 0},
		"image/tiff":      []byte("II*\x00\x08\x00"),
		"image/webp":      []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
	}
	for want, data := range cases {
		got, reason := SniffMIME(data)
		if got != want {
			t.Errorf("SniffMIME(%q) = %q (%s), want %q", data[:4], got, reason, want)
		}
	}
	if got, reason := SniffMIME([]byte("hello world")); got != "" || reason == "" {
		t.Errorf("plain text accepted as %q", got)
	}
	heic := append([]byte{0, 0, 0, 24}, []byte("ftypheic\x00\x00\x00\x00")...)
	if got, reason := SniffMIME(heic); got != "" || !strings.Contains(reason, "HEIC") {
		t.Errorf("heic: got %q reason %q, want an explicit HEIC message", got, reason)
	}
}

func TestImportIsIdempotentPerSourceReference(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := Candidate{Source: "watch", SourceRef: "scan.pdf|20", Filename: "scan.pdf", Data: pdf("one")}

	first, err := f.importer.Import(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != Imported || first.Document.ID != 10000 {
		t.Fatalf("first = %+v", first)
	}
	second, err := f.importer.Import(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != AlreadyImported {
		t.Fatalf("second status = %s, want already_imported", second.Status)
	}
	if n := inboxCount(t, f.st); n != 1 {
		t.Fatalf("inbox has %d documents, want 1", n)
	}
}

func TestSameBytesFromAnotherSourceAreDuplicates(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.importer.Import(ctx, Candidate{Source: "upload", SourceRef: "a.pdf", Filename: "a.pdf", Data: pdf("x")}); err != nil {
		t.Fatal(err)
	}
	res, err := f.importer.Import(ctx, Candidate{Source: "imap", SourceRef: "<m@x>", Filename: "b.pdf", Data: pdf("x")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != Duplicate {
		t.Fatalf("status = %s, want duplicate", res.Status)
	}
	if n := inboxCount(t, f.st); n != 1 {
		t.Fatalf("inbox has %d documents, want 1", n)
	}
}

func TestWatchFolderImportsSettledFilesAndRejectsOthers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	w := &Watcher{Importer: f.importer, Dir: dir, Interval: time.Second}

	past := time.Now().Add(-time.Minute)
	write := func(name string, data []byte, mod time.Time) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	write("scan.pdf", pdf("watch"), past)
	write("readme.txt", []byte("not a document"), past)

	// The first scan only records what it sees. Nothing is read yet.
	if err := w.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, f.st); n != 0 {
		t.Fatalf("file imported on first sight: %d", n)
	}
	if err := w.Scan(ctx); err != nil {
		t.Fatal(err)
	}

	if n := inboxCount(t, f.st); n != 1 {
		t.Fatalf("inbox = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(dir, importedDir, "scan.pdf")); err != nil {
		t.Fatalf("imported file not moved aside: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, rejectedDir, "readme.txt")); err != nil {
		t.Fatalf("unsupported file not moved to rejected: %v", err)
	}
}

func TestWatchFolderWaitsForFreshFiles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	w := &Watcher{Importer: f.importer, Dir: dir, Interval: time.Second}

	p := filepath.Join(dir, "copying.pdf")
	if err := os.WriteFile(p, pdf("slow"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Modified just now: a copy may still be running, so it must wait.
	for i := 0; i < 2; i++ {
		if err := w.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := inboxCount(t, f.st); n != 0 {
		t.Fatalf("fresh file imported while still settling: %d", n)
	}
	past := time.Now().Add(-time.Minute)
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatal(err)
	}
	// The change itself is one scan. The file must then hold still for a second scan.
	if err := w.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, f.st); n != 0 {
		t.Fatalf("file imported in the scan that first saw its change: %d", n)
	}
	if err := w.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, f.st); n != 1 {
		t.Fatalf("settled file not imported: %d", n)
	}
}

// mailWithAttachments builds a multipart message with a PDF, an unsupported text file
// and an inline image with no file name, which is a signature logo and must be skipped.
func mailWithAttachments(messageID string) []byte {
	pdfB64 := base64.StdEncoding.EncodeToString(pdf("mail"))
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nlogo"))
	return []byte(strings.Join([]string{
		"From: landlord@example.org",
		"To: archive@example.org",
		"Subject: Rent review",
		"Message-ID: <" + messageID + ">",
		"Date: Wed, 14 Oct 2026 09:30:00 +0200",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="XYZ"`,
		"",
		"--XYZ",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Please find the letter attached.",
		"--XYZ",
		`Content-Type: application/pdf; name="letter.pdf"`,
		`Content-Disposition: attachment; filename="letter.pdf"`,
		"Content-Transfer-Encoding: base64",
		"",
		pdfB64,
		"--XYZ",
		`Content-Type: text/plain; name="notes.txt"`,
		`Content-Disposition: attachment; filename="notes.txt"`,
		"",
		"plain text, not a document",
		"--XYZ",
		"Content-Type: image/png",
		"Content-Disposition: inline",
		"Content-Transfer-Encoding: base64",
		"",
		png,
		"--XYZ--",
		"",
	}, "\r\n"))
}

func TestParseAttachmentsFindsNamedFilesOnly(t *testing.T) {
	atts, meta, err := ParseAttachments(mailWithAttachments("abc123@example.org"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.MessageID != "abc123@example.org" || meta.Subject != "Rent review" {
		t.Fatalf("meta = %+v", meta)
	}
	if meta.Date.Year() != 2026 || meta.Date.Month() != time.October {
		t.Fatalf("date = %v", meta.Date)
	}
	var names []string
	for _, a := range atts {
		names = append(names, a.Filename)
	}
	if fmt.Sprint(names) != "[letter.pdf notes.txt]" {
		t.Fatalf("attachments = %v, want [letter.pdf notes.txt]", names)
	}
	if !bytes.HasPrefix(atts[0].Data, []byte("%PDF-")) {
		t.Fatalf("pdf bytes not decoded from base64: %q", atts[0].Data[:8])
	}
}

func TestIMAPMessageImportsAttachmentsOnceAndMarksHandled(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := &IMAPPoller{Importer: f.importer, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	raw := mailWithAttachments("dedupe@example.org")

	var first PollSummary
	handled, err := p.processMessage(ctx, raw, &first)
	if err != nil || !handled {
		t.Fatalf("first pass handled=%v err=%v", handled, err)
	}
	if first.Imported != 1 || first.Unsupported != 1 {
		t.Fatalf("first summary = %+v, want 1 imported and 1 unsupported", first)
	}
	doc, err := f.st.GetDocument(ctx, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Rent review" || doc.Source != "imap" || doc.SourceRef != "dedupe@example.org" {
		t.Fatalf("document = %+v", doc)
	}
	if doc.DocumentDate == nil || *doc.DocumentDate != "2026-10-14" {
		t.Fatalf("document date = %v, want 2026-10-14", doc.DocumentDate)
	}

	// The same message again, as after a restart before the mark-as-read reached the server.
	var second PollSummary
	if _, err := p.processMessage(ctx, raw, &second); err != nil {
		t.Fatal(err)
	}
	if second.Imported != 0 || second.Skipped != 1 {
		t.Fatalf("second summary = %+v, want the attachment skipped as already imported", second)
	}
	if n := inboxCount(t, f.st); n != 1 {
		t.Fatalf("inbox = %d after re-poll, want 1", n)
	}
}

func TestIMAPSettingsDefaultsAndValidation(t *testing.T) {
	s := IMAPSettings{Enabled: true, Host: " mail.example.org ", TLS: true, User: "a", Password: "p"}.Normalize()
	if s.Port != 993 || s.Mailbox != "INBOX" || s.PollSeconds != 300 || s.Host != "mail.example.org" {
		t.Fatalf("normalised = %+v", s)
	}
	if err := (IMAPSettings{Enabled: true, Host: "h", User: "u"}).Validate(); err == nil {
		t.Fatal("missing password accepted")
	}
	if err := (IMAPSettings{Enabled: false}).Validate(); err != nil {
		t.Fatalf("disabled settings rejected: %v", err)
	}
}
