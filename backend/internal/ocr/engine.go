package ocr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "image/jpeg"

	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// ErrEngineMissing means a required binary is not installed. Documents are marked
// skipped, not failed, because the fix is to install the tool.
var ErrEngineMissing = errors.New("ocr engine missing")

var errInvalidLanguages = errors.New("languages must look like eng+fra")

const (
	// minPDFTextChars: a PDF with at least this much text layer is not rasterised.
	minPDFTextChars = 20
	// maxPages bounds the work for one scanned PDF.
	maxPages = 40
	// ocrDPI is the highest resolution pages are rasterised at for OCR.
	ocrDPI = 300
	// minRenderDPI is the lowest resolution a scanned PDF is rasterised at. A scan
	// embedded at 150 ppi gains nothing from a 300 dpi render, but a render below
	// this floor loses detail that Tesseract needs.
	minRenderDPI = 150
	// minOrientationConfidence is how sure Tesseract must be before a page is turned
	// to face up. Below it the page is read as it is, so a page with little text is
	// never turned by mistake.
	minOrientationConfidence = 1.5
	// maxRenderEdge caps the longest edge of a rasterised page, in pixels. It is
	// about the longest edge of an A4 page at 300 dpi. Without the cap, a scan on a
	// very large page (poster size) rasterises to hundreds of megapixels, which
	// takes longer than the page time limit.
	maxRenderEdge = 3510
)

// Status is a snapshot of which tools are installed.
type Status struct {
	Tesseract          bool     `json:"tesseract"`
	TesseractVersion   string   `json:"tesseract_version"`
	Poppler            bool     `json:"poppler"`
	InstalledLanguages []string `json:"installed_languages"`
}

// Engine runs the external tools. Empty paths mean the tool is not installed.
type Engine struct {
	Tesseract string
	Pdftotext string
	Pdftoppm  string
	Pdfinfo   string // page count and sizes; required for PDFs
	Pdfimages string // optional: when empty, pages are rasterised at ocrDPI
}

// NewEngine finds the tools on PATH.
func NewEngine() Engine {
	find := func(name string) string {
		p, err := exec.LookPath(name)
		if err != nil {
			return ""
		}
		return p
	}
	return Engine{
		Tesseract: find("tesseract"),
		Pdftotext: find("pdftotext"),
		Pdftoppm:  find("pdftoppm"),
		Pdfinfo:   find("pdfinfo"),
		Pdfimages: find("pdfimages"),
	}
}

// Detect reports which tools are installed and which languages Tesseract has.
func Detect(ctx context.Context, e Engine) Status {
	st := Status{Tesseract: e.Tesseract != "", Poppler: e.Pdftotext != "" && e.Pdftoppm != "" && e.Pdfinfo != ""}
	if !st.Tesseract {
		return st
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := run(ctx, e.Tesseract, "--version"); err == nil {
		firstLine, _, _ := strings.Cut(out, "\n")
		st.TesseractVersion = strings.TrimSpace(strings.TrimPrefix(firstLine, "tesseract"))
	}
	if out, err := run(ctx, e.Tesseract, "--list-langs"); err == nil {
		for i, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if i == 0 || line == "" {
				continue // header line: "List of available languages (n):"
			}
			st.InstalledLanguages = append(st.InstalledLanguages, line)
		}
		sort.Strings(st.InstalledLanguages)
	}
	return st
}

// Extract returns the text of a document. PDFs use the text layer when it has
// enough text. Otherwise each page is rasterised and read on its own, and pageTimeout
// is the time limit for each page: rasterising the page and reading it both count
// against that page's limit. A single image is one page, so it gets the limit once.
//
// turn is how the user has turned the document, in degrees clockwise (0, 90, 180 or
// 270). When it is not 0 it is taken as the way up and page orientation is not
// detected. When it is 0, each page's orientation is detected and corrected.
func (e Engine) Extract(ctx context.Context, path, mime, languages string, turn int, pageTimeout time.Duration) (string, error) {
	if e.Tesseract == "" {
		return "", fmt.Errorf("%w: tesseract is not installed", ErrEngineMissing)
	}
	switch mime {
	case "application/pdf":
		return e.extractPDF(ctx, path, languages, turn, pageTimeout)
	case "image/png", "image/jpeg", "image/tiff", "image/webp":
		pageCtx, cancel := context.WithTimeout(ctx, pageTimeout)
		defer cancel()
		return e.readImage(pageCtx, path, languages, turn)
	}
	return "", fmt.Errorf("no OCR for %s", mime)
}

func (e Engine) extractPDF(ctx context.Context, path, languages string, turn int, pageTimeout time.Duration) (string, error) {
	if e.Pdftotext == "" || e.Pdftoppm == "" || e.Pdfinfo == "" {
		return "", fmt.Errorf("%w: poppler-utils (pdftotext, pdftoppm, pdfinfo) is not installed", ErrEngineMissing)
	}
	text, err := runWithin(ctx, pageTimeout, e.Pdftotext, "-enc", "UTF-8", path, "-")
	if err != nil {
		return "", err
	}
	if len(strings.TrimSpace(text)) >= minPDFTextChars {
		return strings.TrimSpace(text), nil
	}

	info, err := runWithin(ctx, pageTimeout, e.Pdfinfo, "-f", "1", "-l", fmt.Sprint(maxPages), path)
	if err != nil {
		return "", err
	}
	pages := min(pageCount(info), maxPages)
	if pages == 0 {
		return "", errors.New("pdf has no pages to read")
	}
	dpi := e.renderDPI(ctx, path, info, pageTimeout)

	dir, err := os.MkdirTemp("", "archivr-ocr-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	var parts []string
	for page := 1; page <= pages; page++ {
		out, err := e.readPage(ctx, path, dir, page, dpi, languages, turn, pageTimeout)
		if err != nil {
			return "", fmt.Errorf("page %d: %w", page, err)
		}
		if out != "" {
			parts = append(parts, out)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// readPage rasterises one page of the PDF and reads it. Both steps share the page's
// time limit, pageTimeout, so a slow page fails on its own instead of using up the
// time of the pages after it.
func (e Engine) readPage(ctx context.Context, path, dir string, page, dpi int, languages string, turn int, pageTimeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	prefix := filepath.Join(dir, fmt.Sprintf("page-%05d", page))
	if _, err := run(ctx, e.Pdftoppm, "-r", fmt.Sprint(dpi), "-png", "-f", fmt.Sprint(page), "-l", fmt.Sprint(page), path, prefix); err != nil {
		return "", err
	}
	files, err := filepath.Glob(prefix + "*.png")
	if err != nil {
		return "", err
	}
	if len(files) != 1 {
		return "", errors.New("page did not render")
	}
	defer os.Remove(files[0])
	return e.readImage(ctx, files[0], languages, turn)
}

// runWithin is run with its own time limit of budget, on top of ctx.
func runWithin(ctx context.Context, budget time.Duration, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return run(ctx, bin, args...)
}

// pageCount reads the "Pages:" line of pdfinfo output. It returns 0 when there is none.
func pageCount(info string) int {
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(line, "Pages:"); ok {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	return 0
}

// renderDPI returns the resolution to rasterise the PDF at. It starts from the resolution
// of the scanned images (see imageDPI), so a 150 ppi scan is not rendered at 300 dpi and
// upscaled. It is then lowered when the largest page would be wider or taller than
// maxRenderEdge pixels at that resolution. info is the pdfinfo output for the document.
// Where the image resolution cannot be read, ocrDPI is used.
func (e Engine) renderDPI(ctx context.Context, path, info string, budget time.Duration) int {
	dpi := ocrDPI
	if e.Pdfimages != "" {
		if d := e.imageDPI(ctx, path, budget); d > 0 {
			dpi = d
		}
	}
	return min(dpi, dpiForPageSizes(info))
}

// imageDPI returns the resolution of the images in the first maxPages pages, from
// pdfimages -list, or 0 when none can be read. The highest image resolution wins, so a
// page that mixes a 150 ppi scan with a 300 ppi stamp is rendered at 300.
func (e Engine) imageDPI(ctx context.Context, path string, budget time.Duration) int {
	out, err := runWithin(ctx, budget, e.Pdfimages, "-list", "-f", "1", "-l", fmt.Sprint(maxPages), path)
	if err != nil {
		return 0
	}
	return dpiForImageList(out)
}

// dpiForImageList reads the x-ppi column of pdfimages -list and returns the render
// resolution for it: the highest image resolution, kept between minRenderDPI and
// ocrDPI. It returns 0 when the output has no images.
func dpiForImageList(list string) int {
	best := 0
	for _, line := range strings.Split(list, "\n") {
		// Image rows look like "   1     0 image    1275  1650  rgb  3  8  jpeg  no  13  0  150  150  305K  4.9%".
		// The header and the dashed rule are skipped because their type column is not "image".
		fields := strings.Fields(line)
		if len(fields) < 13 || fields[2] != "image" {
			continue
		}
		ppi, err := strconv.Atoi(fields[12])
		if err != nil {
			continue
		}
		best = max(best, ppi)
	}
	if best == 0 {
		return 0
	}
	return max(minRenderDPI, min(ocrDPI, best))
}

// dpiForPageSizes reads the per-page sizes that pdfinfo -f -l prints and returns the
// resolution that keeps the largest page within maxRenderEdge pixels.
func dpiForPageSizes(pdfinfo string) int {
	longest := 0.0
	for _, line := range strings.Split(pdfinfo, "\n") {
		// Per-page lines look like "Page    1 size:  595 x 842 pts (A4)".
		if !strings.HasPrefix(line, "Page") {
			continue
		}
		_, size, found := strings.Cut(line, " size:")
		fields := strings.Fields(size)
		if !found || len(fields) < 3 || fields[1] != "x" {
			continue
		}
		w, errW := strconv.ParseFloat(fields[0], 64)
		h, errH := strconv.ParseFloat(fields[2], 64)
		if errW != nil || errH != nil {
			continue
		}
		longest = max(longest, w, h)
	}
	if longest <= 0 {
		return ocrDPI
	}
	// Page sizes are in points, 72 to the inch.
	fit := int(float64(maxRenderEdge) * 72 / longest)
	return max(1, min(ocrDPI, fit))
}

// readImage reads one page image. The page is turned first: by the document's own
// turn when the user has set one, otherwise by the orientation Tesseract detects.
func (e Engine) readImage(ctx context.Context, input, languages string, turn int) (string, error) {
	deg := turn
	if deg == 0 {
		deg = e.orientation(ctx, input)
	}
	if deg == 0 {
		return e.tesseract(ctx, input, languages)
	}
	turned, err := turnedCopy(input, deg)
	if err != nil {
		if turn != 0 {
			return "", err
		}
		// Detection only suggested a turn and the image cannot be decoded here, so read
		// it as it is rather than fail a document that OCR could still read.
		return e.tesseract(ctx, input, languages)
	}
	defer os.Remove(turned)
	return e.tesseract(ctx, turned, languages)
}

// orientation asks Tesseract how far the page is turned, in degrees clockwise to make
// it upright. It returns 0 when the page is upright, when Tesseract is unsure, or when
// the orientation data is not installed.
func (e Engine) orientation(ctx context.Context, input string) int {
	out, err := run(ctx, e.Tesseract, input, "stdout", "--psm", "0")
	if err != nil {
		return 0
	}
	return parseOrientation(out)
}

// parseOrientation reads the Rotate and Orientation confidence lines of Tesseract's
// orientation output, and returns the turn only when Tesseract is confident in it.
func parseOrientation(out string) int {
	rotate := 0
	confidence := 0.0
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "Rotate":
			rotate, _ = strconv.Atoi(value)
		case "Orientation confidence":
			confidence, _ = strconv.ParseFloat(value, 64)
		}
	}
	rotate = ((rotate % 360) + 360) % 360
	if rotate%90 != 0 || confidence < minOrientationConfidence {
		return 0
	}
	return rotate
}

// turnedCopy writes the image at input, turned clockwise by deg, to a new temporary
// PNG and returns its path. The original file is not changed.
func turnedCopy(input string, deg int) (string, error) {
	f, err := os.Open(input)
	if err != nil {
		return "", err
	}
	src, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return "", fmt.Errorf("turn page: %w", err)
	}
	out, err := os.CreateTemp("", "archivr-turn-*.png")
	if err != nil {
		return "", err
	}
	if err := png.Encode(out, rotate(src, deg)); err != nil {
		out.Close()
		os.Remove(out.Name())
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

// rotate returns src turned clockwise by deg, which must be 90, 180 or 270.
func rotate(src image.Image, deg int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	size := image.Rect(0, 0, w, h)
	if deg != 180 {
		size = image.Rect(0, 0, h, w) // a quarter turn swaps the width and height
	}
	dst := image.NewRGBA(size)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := src.At(b.Min.X+x, b.Min.Y+y)
			switch deg {
			case 90:
				dst.Set(h-1-y, x, c)
			case 180:
				dst.Set(w-1-x, h-1-y, c)
			case 270:
				dst.Set(y, w-1-x, c)
			}
		}
	}
	return dst
}

// tesseract runs OCR on one image file.
func (e Engine) tesseract(ctx context.Context, input, languages string) (string, error) {
	out, err := run(ctx, e.Tesseract, input, "stdout", "-l", languages)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// run executes a tool and returns stdout. Stderr is folded into the error text.
func run(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%s stopped: %w", filepath.Base(bin), ctx.Err())
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("%s failed: %v %s", filepath.Base(bin), err, msg)
	}
	return stdout.String(), nil
}
