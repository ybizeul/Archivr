package preview

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestImageThumbnailFitsTheBox(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "scan.png")
	writePNG(t, src, 1200, 600)

	c := &Cache{Dir: filepath.Join(dir, "cache")}
	sha := "ab" + "cdef0123456789"
	out, err := c.Thumbnail(context.Background(), sha, src, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != Side || cfg.Height != Side/2 {
		t.Fatalf("thumbnail %dx%d, want %dx%d", cfg.Width, cfg.Height, Side, Side/2)
	}

	// The second call reads the cache and does not render again.
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	again, err := c.Thumbnail(context.Background(), sha, src, "image/png")
	if err != nil || again != out {
		t.Fatalf("cached thumbnail not reused: %v", err)
	}
}

func TestPDFThumbnailNeedsPoppler(t *testing.T) {
	c := &Cache{Dir: t.TempDir()}
	_, err := c.Thumbnail(context.Background(), "abcdef", "x.pdf", "application/pdf")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestUnknownTypeHasNoPreview(t *testing.T) {
	c := &Cache{Dir: t.TempDir()}
	_, err := c.Thumbnail(context.Background(), "abcdef", "x.bin", "application/octet-stream")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}
