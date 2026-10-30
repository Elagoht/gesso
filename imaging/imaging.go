// Package imaging makes an uploaded image fit to keep: it reads PNG, JPEG, GIF
// and WebP, shrinks or crops it to a size, and writes it again as JPEG or PNG.
// Writing it again drops what the file carried besides the picture — EXIF, a
// phone's GPS position — and anything that was not an image at all.
package imaging

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Options is what Process does to an image.
type Options struct {
	// Width and Height bound the result. Zero leaves that side free.
	Width, Height int
	// Fill crops the image to exactly Width × Height, from the middle, instead
	// of fitting it inside them. It needs both.
	Fill bool
	// Format is "jpeg" or "png"; "" keeps a PNG or GIF as PNG and the rest as
	// JPEG.
	Format string
	// Quality is JPEG's, 1 to 100. Zero: 85.
	Quality int
	// MaxPixels refuses an image larger than this many pixels before it is
	// decoded — a small file can claim a huge picture. Zero: 40 million.
	MaxPixels int
}

// Image is a processed image.
type Image struct {
	Data        []byte
	ContentType string // image/jpeg or image/png
	Ext         string // .jpg or .png
}

// ErrNotImage is a file that is not a PNG, JPEG, GIF or WebP image.
var ErrNotImage = errors.New("imaging: not a PNG, JPEG, GIF or WebP image")

// ErrTooLarge is an image of more pixels than Options.MaxPixels.
var ErrTooLarge = errors.New("imaging: the image has too many pixels")

// Process reads an image from r and writes it again as o says.
func Process(r io.Reader, o Options) (Image, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Image{}, err
	}
	decode, format, err := decoder(data)
	if err != nil {
		return Image{}, err
	}
	cfg, err := decode.config(bytes.NewReader(data))
	if err != nil {
		return Image{}, ErrNotImage
	}
	maxPixels := o.MaxPixels
	if maxPixels <= 0 {
		maxPixels = 40_000_000
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxPixels/cfg.Height {
		return Image{}, ErrTooLarge
	}
	src, err := decode.image(bytes.NewReader(data))
	if err != nil {
		return Image{}, ErrNotImage
	}
	if o.Fill && (o.Width <= 0 || o.Height <= 0) {
		return Image{}, errors.New("imaging: Fill needs a Width and a Height")
	}
	img := resize(src, o)
	out := o.Format
	if out == "" {
		out = "jpeg"
		if format == "png" || format == "gif" {
			out = "png"
		}
	}
	var buf bytes.Buffer
	switch out {
	case "jpeg":
		q := o.Quality
		if q <= 0 || q > 100 {
			q = 85
		}
		if err := jpeg.Encode(&buf, flatten(img), &jpeg.Options{Quality: q}); err != nil {
			return Image{}, err
		}
		return Image{buf.Bytes(), "image/jpeg", ".jpg"}, nil
	case "png":
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, img); err != nil {
			return Image{}, err
		}
		return Image{buf.Bytes(), "image/png", ".png"}, nil
	}
	return Image{}, fmt.Errorf("imaging: unknown format %q: jpeg or png", out)
}

type decoderFuncs struct {
	config func(io.Reader) (image.Config, error)
	image  func(io.Reader) (image.Image, error)
}

// decoder is the decoder for data's format, by its first bytes.
func decoder(data []byte) (decoderFuncs, string, error) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return decoderFuncs{png.DecodeConfig, png.Decode}, "png", nil
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return decoderFuncs{jpeg.DecodeConfig, jpeg.Decode}, "jpeg", nil
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return decoderFuncs{gif.DecodeConfig, gif.Decode}, "gif", nil
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return decoderFuncs{webp.DecodeConfig, webp.Decode}, "webp", nil
	}
	return decoderFuncs{}, "", ErrNotImage
}

// resize shrinks src to fit inside, or to fill, o's size; it never enlarges.
func resize(src image.Image, o Options) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if o.Fill {
		// Crop the middle to the target's proportions, then scale.
		cw, ch := w, w*o.Height/o.Width
		if ch > h {
			cw, ch = h*o.Width/o.Height, h
		}
		x, y := b.Min.X+(w-cw)/2, b.Min.Y+(h-ch)/2
		crop := image.Rect(x, y, x+cw, y+ch)
		tw, th := min(o.Width, cw), min(o.Height, ch)
		dst := image.NewRGBA(image.Rect(0, 0, tw, th))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, draw.Src, nil)
		return dst
	}
	scale := 1.0
	if o.Width > 0 && w > o.Width {
		scale = float64(o.Width) / float64(w)
	}
	if o.Height > 0 && float64(h)*scale > float64(o.Height) {
		scale = float64(o.Height) / float64(h)
	}
	tw, th := max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// flatten puts img on white: JPEG has no transparency.
func flatten(img image.Image) image.Image {
	dst := image.NewRGBA(img.Bounds())
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Over)
	return dst
}
