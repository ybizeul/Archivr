package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// truncatedPDF is a whole PDF cut short before its cross-reference table, as in the
// copy that was imported from the watch folder.
func truncatedPDF() []byte {
	return []byte("%PDF-1.5\n%\x80\x80\x80\x80\n1 0 obj\n<< /Type /Catalog >>\nendobj\n" +
		"2 0 obj\n<< /Length 4 >>\nstream\nabcd\nendstream\nendobj\n")
}

func TestIncompletePDFDetectsCutShortCopies(t *testing.T) {
	whole := pdf("whole")
	if reason := IncompletePDF(whole); reason != "" {
		t.Fatalf("whole PDF refused: %s", reason)
	}
	if reason := IncompletePDF(truncatedPDF()); reason == "" {
		t.Fatal("PDF cut before its end-of-file marker was accepted")
	}
	// Trailing bytes after %%EOF are tolerated.
	withJunk := append(pdf("junk"), []byte("\x00\x00 trailing\n")...)
	if reason := IncompletePDF(withJunk); reason != "" {
		t.Fatalf("PDF with trailing bytes refused: %s", reason)
	}
	// Other types are not this check's business.
	if reason := IncompletePDF([]byte("\x89PNG\r\n\x1a\n....")); reason != "" {
		t.Fatalf("png judged as incomplete PDF: %s", reason)
	}
}

func TestImportRefusesIncompletePDF(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	res, err := f.importer.Import(ctx, Candidate{Source: "upload", SourceRef: "cut.pdf", Filename: "cut.pdf", Data: truncatedPDF()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != Unsupported {
		t.Fatalf("status = %s, want unsupported", res.Status)
	}
	if n := inboxCount(t, f.st); n != 0 {
		t.Fatalf("incomplete PDF filed: inbox = %d", n)
	}
}

func TestWatchFolderHoldsIncompletePDFUntilItIsWhole(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	w := &Watcher{Importer: f.importer, Dir: dir, Interval: time.Second}
	p := filepath.Join(dir, "late.pdf")

	past := time.Now().Add(-time.Minute)
	if err := os.WriteFile(p, truncatedPDF(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatal(err)
	}
	// Two scans settle the file, then it is judged incomplete and held.
	for i := 0; i < 3; i++ {
		if err := w.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := inboxCount(t, f.st); n != 0 {
		t.Fatalf("incomplete PDF imported: inbox = %d", n)
	}
	// It stays in place, so a writer that resumes still has its file.
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("held file was moved: %v", err)
	}

	// The copy finishes: the file changes, and is imported once it settles.
	if err := os.WriteFile(p, pdf("late"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(-30 * time.Second)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := w.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := inboxCount(t, f.st); n != 1 {
		t.Fatalf("completed PDF not imported: inbox = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(dir, importedDir, "late.pdf")); err != nil {
		t.Fatalf("completed file not moved aside: %v", err)
	}
}
