package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, 0, color.NRGBA{R: 255, A: 128})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func size(t *testing.T, im Image) (int, int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(im.Data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width, cfg.Height
}

func TestProcess(t *testing.T) {
	src := pngOf(t, 400, 200)
	im, err := Process(bytes.NewReader(src), Options{Width: 100})
	if err != nil || im.ContentType != "image/png" || im.Ext != ".png" {
		t.Fatalf("%v %+v", err, im.ContentType)
	}
	if w, h := size(t, im); w != 100 || h != 50 {
		t.Fatalf("fit: %dx%d", w, h)
	}
	im, _ = Process(bytes.NewReader(src), Options{Width: 50, Height: 50, Fill: true, Format: "jpeg"})
	if w, h := size(t, im); w != 50 || h != 50 || im.ContentType != "image/jpeg" {
		t.Fatalf("fill: %dx%d %s", w, h, im.ContentType)
	}
	if _, err := jpeg.Decode(bytes.NewReader(im.Data)); err != nil {
		t.Fatal(err)
	}
	im, _ = Process(bytes.NewReader(src), Options{Width: 1000})
	if w, _ := size(t, im); w != 400 {
		t.Fatalf("an image is never enlarged: %d", w)
	}
	if _, err := Process(strings.NewReader("<svg xmlns='http://www.w3.org/2000/svg'/>"), Options{}); !errors.Is(err, ErrNotImage) {
		t.Fatalf("svg: %v", err)
	}
	if _, err := Process(bytes.NewReader(src), Options{MaxPixels: 1000}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too many pixels: %v", err)
	}
	if _, err := Process(bytes.NewReader(src), Options{Width: 10, Fill: true}); err == nil {
		t.Fatal("fill without a height")
	}
}
