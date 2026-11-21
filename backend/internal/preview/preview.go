// Package preview makes cached thumbnails of documents: the first page of a PDF,
// or a scaled-down image.
//
// The cache is keyed by the document's SHA-256. The bytes of a document never
// change, so a cached thumbnail never goes stale and needs no invalidation.
package preview

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/image/draw"
	// Register the decoders for the formats the importer accepts.
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	_ "image/png"
)

// Side is the longest edge of a thumbnail, in pixels.
const Side = 480

// ErrUnavailable means no thumbnail can be made, for example because poppler is missing.
var ErrUnavailable = errors.New("thumbnail unavailable")

// Cache creates and serves thumbnails under Dir.
type Cache struct {
	Dir      string
	Pdftoppm string // empty when poppler is not installed
}

// Thumbnail returns the path of the cached JPEG for a document, creating it if needed.
func (c *Cache) Thumbnail(ctx context.Context, sha, src, mime string) (string, error) {
	if len(sha) < 2 {
		return "", fmt.Errorf("bad hash")
	}
	dst := filepath.Join(c.Dir, sha[:2], fmt.Sprintf("%s-%d.jpg", sha, Side))
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}

	tmp := dst + ".tmp"
	var err error
	switch mime {
	case "application/pdf":
		err = c.renderPDF(ctx, src, tmp)
	case "image/png", "image/jpeg", "image/tiff", "image/webp":
		err = renderImage(src, tmp)
	default:
		err = fmt.Errorf("%w: no preview for %s", ErrUnavailable, mime)
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

func (c *Cache) renderPDF(ctx context.Context, src, dst string) error {
	if c.Pdftoppm == "" {
		return fmt.Errorf("%w: poppler-utils is not installed", ErrUnavailable)
	}
	// pdftoppm appends the extension to the prefix, so write to a prefix and rename.
	prefix := dst + "-page"
	cmd := exec.CommandContext(ctx, c.Pdftoppm,
		"-f", "1", "-l", "1", "-jpeg", "-singlefile",
		"-scale-to", fmt.Sprint(Side), src, prefix)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pdftoppm: %v: %s", err, out)
	}
	produced := prefix + ".jpg"
	defer os.Remove(produced)
	return os.Rename(produced, dst)
}

func renderImage(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return fmt.Errorf("decode image: %w", err)
	}
	scaled := fit(img, Side)
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(out, scaled, &jpeg.Options{Quality: 82}); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// fit scales img so its longest edge is at most max, keeping the aspect ratio.
func fit(img image.Image, max int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return img
	}
	nw, nh := w, h
	if w >= h {
		nw, nh = max, h*max/w
	} else {
		nw, nh = w*max/h, max
	}
	if nh < 1 {
		nh = 1
	}
	if nw < 1 {
		nw = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}
