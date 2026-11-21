package ocr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
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
	// ocrDPI is the resolution pages are rasterised at for OCR.
	ocrDPI = 300
	// maxRenderEdge caps the longest edge of a rasterised page, in pixels. It is
	// about the longest edge of an A4 page at 300 dpi. Without the cap, a scan on a
	// very large page (poster size) rasterises to hundreds of megapixels, which
	// takes longer than the job timeout.
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
	Pdfinfo   string // optional: when empty, pages are rasterised at ocrDPI
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
	}
}

// Detect reports which tools are installed and which languages Tesseract has.
func Detect(ctx context.Context, e Engine) Status {
	st := Status{Tesseract: e.Tesseract != "", Poppler: e.Pdftotext != "" && e.Pdftoppm != ""}
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
// enough text, and otherwise rasterise each page (at ocrDPI, or less for very large
// pages) and run OCR on it.
func (e Engine) Extract(ctx context.Context, path, mime, languages string) (string, error) {
	if e.Tesseract == "" {
		return "", fmt.Errorf("%w: tesseract is not installed", ErrEngineMissing)
	}
	switch mime {
	case "application/pdf":
		return e.extractPDF(ctx, path, languages)
	case "image/png", "image/jpeg", "image/tiff", "image/webp":
		return e.tesseract(ctx, path, languages)
	}
	return "", fmt.Errorf("no OCR for %s", mime)
}

func (e Engine) extractPDF(ctx context.Context, path, languages string) (string, error) {
	if e.Pdftotext == "" || e.Pdftoppm == "" {
		return "", fmt.Errorf("%w: poppler-utils (pdftotext, pdftoppm) is not installed", ErrEngineMissing)
	}
	text, err := run(ctx, e.Pdftotext, "-enc", "UTF-8", path, "-")
	if err != nil {
		return "", err
	}
	if len(strings.TrimSpace(text)) >= minPDFTextChars {
		return strings.TrimSpace(text), nil
	}

	dir, err := os.MkdirTemp("", "archivr-ocr-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	prefix := filepath.Join(dir, "page")
	dpi := e.pageDPI(ctx, path)
	if _, err := run(ctx, e.Pdftoppm, "-r", fmt.Sprint(dpi), "-png", "-l", fmt.Sprint(maxPages), path, prefix); err != nil {
		return "", err
	}
	pages, err := filepath.Glob(prefix + "*.png")
	if err != nil {
		return "", err
	}
	sort.Strings(pages)
	if len(pages) == 0 {
		return "", errors.New("pdf has no pages to read")
	}
	var parts []string
	for _, p := range pages {
		out, err := e.tesseract(ctx, p, languages)
		if err != nil {
			return "", err
		}
		if out != "" {
			parts = append(parts, out)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// pageDPI returns the resolution to rasterise the PDF at: ocrDPI, lowered when the
// largest page would be wider or taller than maxRenderEdge pixels at that resolution.
// If the page sizes cannot be read, it returns ocrDPI.
func (e Engine) pageDPI(ctx context.Context, path string) int {
	if e.Pdfinfo == "" {
		return ocrDPI
	}
	out, err := run(ctx, e.Pdfinfo, "-f", "1", "-l", fmt.Sprint(maxPages), path)
	if err != nil {
		return ocrDPI
	}
	return dpiForPageSizes(out)
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
