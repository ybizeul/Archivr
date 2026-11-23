package ocr

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
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
	_, err := Engine{}.Extract(context.Background(), "x.pdf", "application/pdf", "eng", 0)
	if err == nil {
		t.Fatal("no error from an engine without tesseract")
	}
}

func TestDPIForPageSizesCapsLargePages(t *testing.T) {
	cases := []struct {
		name string
		info string
		want int
	}{
		{"A4 keeps 300 dpi", "Page    1 size:  595 x 842 pts (A4)\n", 300},
		{"a small page is not upscaled", "Page    1 size:  288 x 432 pts\n", 300},
		{"poster-size scan is lowered", "Page    1 size:  3024 x 4032 pts\nPage    2 size:  3024 x 4032 pts\n", 62},
		{"largest page of the document counts", "Page    1 size:  595 x 842 pts (A4)\nPage    2 size:  3024 x 4032 pts\n", 62},
		{"unreadable output falls back to 300", "File size: 10 bytes\n", 300},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dpiForPageSizes(c.info); got != c.want {
				t.Fatalf("dpi = %d, want %d", got, c.want)
			}
		})
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
	text, err := e.Extract(ctx, file, "image/png", "eng", 0)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(strings.ToLower(text), "electricity") {
		t.Fatalf("OCR text = %q, want it to contain ELECTRICITY", text)
	}
}

// A page scanned sideways is read by detecting its orientation, and also when the
// user has turned it by hand. Both turns of the same page must give the same text.
func TestTesseractReadsSidewaysText(t *testing.T) {
	e := NewEngine()
	if e.Tesseract == "" {
		t.Skip("tesseract not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Orientation detection needs a page with several lines of text, as a scan has.
	// One line is too little for Tesseract to judge the orientation.
	upright := renderLines(t, "ELECTRICITY BILL 2026", "Total due 42 euros", "Payment before 14 October", "Account number 88213")
	// Text that runs bottom to top: the page is turned 90 degrees counter-clockwise,
	// so it takes a clockwise turn of 90 to face up.
	sideways := encodePNG(t, rotate(decodePNG(t, upright), 270))
	file := filepath.Join(t.TempDir(), "sideways.png")
	if err := os.WriteFile(file, sideways, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, turn := range []int{0, 90} {
		text, err := e.Extract(ctx, file, "image/png", "eng", turn)
		if err != nil {
			t.Fatalf("turn %d: extract: %v", turn, err)
		}
		if !strings.Contains(strings.ToLower(text), "electricity") {
			t.Fatalf("turn %d: OCR text = %q, want it to contain ELECTRICITY", turn, text)
		}
	}
}

// Tesseract's orientation output: Rotate is the clockwise turn that makes the page upright.
func TestParseOrientation(t *testing.T) {
	const sideways = `Page number: 0
Orientation in degrees: 270
Rotate: 90
Orientation confidence: 2.08
Script: Latin
Script confidence: 0.95
`
	const upright = `Page number: 0
Orientation in degrees: 0
Rotate: 0
Orientation confidence: 2.28
`
	cases := []struct {
		name string
		out  string
		want int
	}{
		{"sideways page is turned", sideways, 90},
		{"upright page is left alone", upright, 0},
		{"a turn of 270 is kept", "Rotate: 270\nOrientation confidence: 1.89\n", 270},
		{"unsure detection is ignored", "Rotate: 180\nOrientation confidence: 0.4\n", 0},
		{"unreadable output is ignored", "Too few characters. Skipping this page\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseOrientation(c.out); got != c.want {
				t.Fatalf("turn = %d, want %d", got, c.want)
			}
		})
	}
}

// rotate turns clockwise: the top-left pixel ends up top-right, and the top-right
// pixel ends up bottom-right, for a quarter turn. The size swaps for a quarter turn.
func TestRotateTurnsClockwise(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	topLeft := color.RGBA{R: 255, A: 255}
	topRight := color.RGBA{G: 255, A: 255}
	src.Set(0, 0, topLeft)
	src.Set(2, 0, topRight)

	q := rotate(src, 90)
	if q.Bounds().Dx() != 2 || q.Bounds().Dy() != 3 {
		t.Fatalf("size after 90 = %v, want 2x3", q.Bounds())
	}
	if q.At(1, 0) != topLeft || q.At(1, 2) != topRight {
		t.Fatalf("90: got %v and %v at the right edge", q.At(1, 0), q.At(1, 2))
	}

	// A half turn puts the top-left pixel at the bottom-right, and keeps the size.
	h := rotate(src, 180)
	if h.Bounds().Dx() != 3 || h.Bounds().Dy() != 2 || h.At(2, 1) != topLeft {
		t.Fatalf("180: size %v, bottom-right %v", h.Bounds(), h.At(2, 1))
	}

	// Three quarter turns clockwise is one counter-clockwise turn, which undoes 90.
	back := rotate(q, 270)
	if back.Bounds() != src.Bounds() || back.At(0, 0) != topLeft || back.At(2, 0) != topRight {
		t.Fatalf("270 after 90 did not restore the page")
	}
}

func decodePNG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
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

// renderLines draws a page of text, an A4 sheet at 150 dpi, with a proportional font
// as a scan would show it. Each line starts on its own row.
func renderLines(t *testing.T, lines ...string) []byte {
	t.Helper()
	ttf, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(ttf, &opentype.FaceOptions{Size: 28, DPI: 150})
	if err != nil {
		t.Fatal(err)
	}
	page := image.NewRGBA(image.Rect(0, 0, 1240, 1754))
	draw.Draw(page, page.Bounds(), image.White, image.Point{}, draw.Src)
	d := &font.Drawer{Dst: page, Src: image.NewUniform(color.Black), Face: face}
	for i, line := range lines {
		d.Dot = fixed.P(120, 200+i*110)
		d.DrawString(line)
	}
	return encodePNG(t, page)
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
