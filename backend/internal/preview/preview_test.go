package preview

import (
	"context"
	"errors"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"os"
	"os/exec"
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

// testdata/cropped.pdf has a 400x400 media box and a 200x200 crop box. The crop box holds only
// red, so a thumbnail that honours it is red everywhere; one that ignores it shows white corners.
func TestPDFThumbnailShowsTheCropBox(t *testing.T) {
	pdftoppm, err := exec.LookPath("pdftoppm")
	if err != nil {
		t.Skip("poppler-utils is not installed")
	}
	c := &Cache{Dir: t.TempDir(), Pdftoppm: pdftoppm}
	out, err := c.Thumbnail(context.Background(), "abcdef12", filepath.Join("testdata", "cropped.pdf"), "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	for _, p := range [][2]int{{b.Min.X + 5, b.Min.Y + 5}, {b.Max.X - 6, b.Max.Y - 6}, {b.Min.X + b.Dx()/2, b.Min.Y + b.Dy()/2}} {
		r, g, bl, _ := img.At(p[0], p[1]).RGBA()
		if r>>8 < 200 || g>>8 > 80 || bl>>8 > 80 {
			t.Fatalf("pixel %v = (%d,%d,%d), want red: the crop box was not rendered", p, r>>8, g>>8, bl>>8)
		}
	}
}

func TestUnknownTypeHasNoPreview(t *testing.T) {
	c := &Cache{Dir: t.TempDir()}
	_, err := c.Thumbnail(context.Background(), "abcdef", "x.bin", "application/octet-stream")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}
