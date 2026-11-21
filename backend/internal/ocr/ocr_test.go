package ocr

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/archivr/backend/internal/archive"
	"github.com/archivr/backend/internal/db"
	"github.com/archivr/backend/internal/storage"
	"github.com/archivr/backend/internal/store"
)

func newStore(t *testing.T) (*store.Store, *archive.Service, storage.Layout) {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(context.Background(), filepath.Join(dir, "archivr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	st := store.New(conn)
	layout := storage.Layout{DataDir: dir}
	svc := &archive.Service{Store: st, Layout: layout, Log: quietLog()}
	return st, svc, layout
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSettingsDefaultsAndLanguageValidation(t *testing.T) {
	s := Settings{Languages: "  "}.Normalize()
	if s.Languages != "eng+fra" || s.TimeoutSeconds != 30 {
		t.Fatalf("normalised = %+v", s)
	}
	if err := (Settings{Languages: "eng+fra"}).Validate(); err != nil {
		t.Fatalf("eng+fra rejected: %v", err)
	}
	for _, bad := range []string{"eng; rm -rf /", "-psm", "eng fra", "../x"} {
		if err := (Settings{Languages: bad}).Validate(); err == nil {
			t.Errorf("languages %q accepted", bad)
		}
	}
}

// A pending document must be processed without anyone calling Enqueue, because the
// scanner re-queues pending rows on start. With no tesseract, the job ends as
// skipped with a reason, and the app keeps running.
func TestPendingDocumentsAreRequeuedAndSkippedWithoutEngine(t *testing.T) {
	st, svc, layout := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doc, err := svc.Create(ctx, archive.NewDocument{
		Title: "Scan", Data: []byte("%PDF-1.4\n%%EOF\n"), Mime: "application/pdf", Source: "upload",
	})
	if err != nil {
		t.Fatal(err)
	}

	pool := NewPool(st, layout, Engine{}, 1, quietLog())
	pool.Start(ctx)
	defer func() {
		cancel()
		pool.Wait()
	}()

	got := waitForStatus(t, st, doc.ID, "skipped")
	if got.OCRError == "" {
		t.Fatal("skipped document has no reason")
	}
}

func TestEngineWithoutTesseractReportsMissing(t *testing.T) {
	_, err := Engine{}.Extract(context.Background(), "x.pdf", "application/pdf", "eng")
	if err == nil {
		t.Fatal("no error from an engine without tesseract")
	}
}

func TestDetectReportsMissingTools(t *testing.T) {
	st := Detect(context.Background(), Engine{})
	if st.Tesseract || st.Poppler {
		t.Fatalf("status = %+v, want nothing installed", st)
	}
}

// When tesseract is installed, read a rendered image. Skipped otherwise.
func TestTesseractReadsRenderedText(t *testing.T) {
	e := NewEngine()
	if e.Tesseract == "" {
		t.Skip("tesseract not installed")
	}
	_, svc, layout := newStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	doc, err := svc.Create(ctx, archive.NewDocument{
		Title: "Bill", Data: renderText(t, "ELECTRICITY BILL 2026"), Mime: "image/png", Source: "upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := layout.DocumentFile(doc.Folder.Path, doc.Filename)
	if err != nil {
		t.Fatal(err)
	}
	text, err := e.Extract(ctx, file, "image/png", "eng")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(strings.ToLower(text), "electricity") {
		t.Fatalf("OCR text = %q, want it to contain ELECTRICITY", text)
	}
}

// renderText draws text in a basic font, scaled up so Tesseract can read it.
func renderText(t *testing.T, s string) []byte {
	t.Helper()
	small := image.NewRGBA(image.Rect(0, 0, 300, 40))
	draw.Draw(small, small.Bounds(), image.White, image.Point{}, draw.Src)
	d := &font.Drawer{
		Dst:  small,
		Src:  image.NewUniform(color.Black),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(10, 26),
	}
	d.DrawString(s)

	big := image.NewRGBA(image.Rect(0, 0, small.Bounds().Dx()*4, small.Bounds().Dy()*4))
	draw.NearestNeighbor.Scale(big, big.Bounds(), small, small.Bounds(), draw.Src, nil)

	var buf bytes.Buffer
	if err := png.Encode(&buf, big); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func waitForStatus(t *testing.T, st *store.Store, id int64, want string) store.Document {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		doc, err := st.GetDocument(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if doc.OCRStatus == want {
			return doc
		}
		if time.Now().After(deadline) {
			t.Fatalf("document %d status = %s, want %s", id, doc.OCRStatus, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
