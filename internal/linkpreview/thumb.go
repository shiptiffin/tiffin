package linkpreview

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Thumb is the small copy of a share image the dashboard shows.
type Thumb struct {
	MIME string // image/jpeg, or image/png when the image is see-through
	Data []byte
	Hash string // the source image's hash
}

const (
	thumbWidth = 800      // the dashboard's cards are at most 560 px wide; 800 is sharp at 1.5×
	maxPixels  = 40 << 20 // larger images are measured, not decoded
)

// makeThumb measures an image and scales it to thumbWidth. On an error
// after measuring, w and h are still set.
func makeThumb(b []byte) (th *Thumb, w, h int, err error) {
	var cfg image.Config
	var decode func([]byte) (image.Image, error)
	switch sniffImage(b) {
	case "image/png":
		cfg, err = png.DecodeConfig(bytes.NewReader(b))
		decode = func(b []byte) (image.Image, error) { return png.Decode(bytes.NewReader(b)) }
	case "image/jpeg":
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(b))
		decode = func(b []byte) (image.Image, error) { return jpeg.Decode(bytes.NewReader(b)) }
	case "image/gif":
		cfg, err = gif.DecodeConfig(bytes.NewReader(b))
		decode = func(b []byte) (image.Image, error) { return gif.Decode(bytes.NewReader(b)) } // the first frame, as unfurlers show
	case "image/webp":
		cfg, err = webp.DecodeConfig(bytes.NewReader(b))
		decode = func(b []byte) (image.Image, error) { return webp.Decode(bytes.NewReader(b)) }
	default:
		return nil, 0, 0, errors.New("the box can't read this kind of image")
	}
	if err != nil {
		return nil, 0, 0, errors.New("the image could not be read: " + err.Error())
	}
	w, h = cfg.Width, cfg.Height
	if w < 1 || h < 1 {
		return nil, 0, 0, errors.New("the image is empty")
	}
	if w*h > maxPixels {
		return nil, w, h, errors.New("too large to make a small copy of")
	}
	src, err := decode(b)
	if err != nil {
		return nil, w, h, errors.New("the image could not be read: " + err.Error())
	}
	tw, thh := w, h
	if tw > thumbWidth {
		tw, thh = thumbWidth, max(1, h*thumbWidth/w)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, tw, thh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	var buf bytes.Buffer
	th = &Thumb{}
	if dst.Opaque() {
		th.MIME = "image/jpeg"
		err = jpeg.Encode(&buf, flatten(dst), &jpeg.Options{Quality: 82})
	} else {
		th.MIME = "image/png"
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, dst)
	}
	if err != nil {
		return nil, w, h, err
	}
	th.Data = buf.Bytes()
	return th, w, h, nil
}

// flatten turns an opaque NRGBA image into RGBA for the JPEG encoder.
func flatten(src *image.NRGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Over)
	return dst
}
