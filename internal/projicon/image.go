package projicon

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Limits on any icon the box takes in, inferred or uploaded.
const (
	// MaxBytes is the most an icon file may weigh.
	MaxBytes = 512 << 10
	// maxSide bounds a raster's decoded size, so a small file can't
	// unpack into a huge bitmap.
	maxSide = 2048
	// storeSide is the largest raster kept: big enough for a 40 px icon on
	// a 3x screen, and for email at 192 px.
	storeSide = 256
	// EmailSide is the size of the PNG served for email.
	EmailSide = 192
	// GoodSide is the smallest raster that counts as a good icon.
	GoodSide = 64
)

// Image is an icon ready to store: a sanitised SVG, or a PNG re-encoded
// from whatever came in (which also drops metadata and anything hidden
// after the image data).
type Image struct {
	MIME string // image/png or image/svg+xml
	Data []byte
	// Width and Height are the source's size in pixels (rasters), or its
	// viewBox (SVG). The stored PNG is at most storeSide.
	Width, Height int
	// Tone says what the icon's visible pixels are when it has a
	// transparent background: "dark", "light" or "" (mixed, opaque, SVG).
	// A dark icon needs a light plate on a dark page, and the other way.
	Tone string
}

// Hash identifies an icon's stored bytes.
func (i *Image) Hash() string { return hashOf(i.Data) }

func hashOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])[:20]
}

// SVG reports whether the icon is vector.
func (i *Image) SVG() bool { return i.MIME == mimeSVG }

// Side is the icon's smaller side: how sharp it can be drawn.
func (i *Image) Side() int { return min(i.Width, i.Height) }

const (
	mimePNG = "image/png"
	mimeSVG = "image/svg+xml"
)

// Errors an icon is refused with. Each reads as a sentence for people.
var (
	ErrTooLarge = fmt.Errorf("the image is larger than %d KB", MaxBytes>>10)
	ErrNotImage = errors.New("the file is not a PNG, JPEG, GIF, WebP, ICO or SVG image")
	ErrTooBig   = fmt.Errorf("the image is larger than %d×%d pixels", maxSide, maxSide)
)

// Sniff names an image format from its first bytes ("" when it is none
// the box takes). The Content-Type a server sends is not trusted for this.
func Sniff(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		return "jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp"
	case len(b) >= 6 && b[0] == 0 && b[1] == 0 && b[2] == 1 && b[3] == 0 && (b[4] != 0 || b[5] != 0):
		return "ico"
	case looksSVG(b):
		return "svg"
	}
	return ""
}

// Normalize checks an icon and turns it into what the box stores: an SVG
// passes the safety pass (SanitizeSVG), and every raster format is decoded
// and re-encoded as a PNG at most storeSide square.
func Normalize(b []byte) (*Image, error) {
	if len(b) > MaxBytes {
		return nil, ErrTooLarge
	}
	kind := Sniff(b)
	if kind == "" {
		return nil, ErrNotImage
	}
	if kind == "svg" {
		out, w, h, err := SanitizeSVG(b)
		if err != nil {
			return nil, err
		}
		return &Image{MIME: mimeSVG, Data: out, Width: w, Height: h}, nil
	}
	src, err := decodeRaster(kind, b)
	if err != nil {
		return nil, err
	}
	return fromRaster(src)
}

// NormalizeRaster is Normalize for a raster only (a PNG to stand in for an
// SVG where SVG can't go, such as email).
func NormalizeRaster(b []byte) (*Image, error) {
	if k := Sniff(b); k == "svg" {
		return nil, errors.New("the stand-in for an SVG icon must be a raster image (PNG, JPEG, GIF, WebP or ICO)")
	}
	return Normalize(b)
}

func decodeRaster(kind string, b []byte) (image.Image, error) {
	if kind == "ico" {
		return decodeICO(b)
	}
	var cfg image.Config
	var err error
	switch kind {
	case "png":
		cfg, err = png.DecodeConfig(bytes.NewReader(b))
	case "jpeg":
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(b))
	case "gif":
		cfg, err = gif.DecodeConfig(bytes.NewReader(b))
	case "webp":
		cfg, err = webp.DecodeConfig(bytes.NewReader(b))
	}
	if err != nil {
		return nil, fmt.Errorf("the %s image could not be read: %w", strings.ToUpper(kind), err)
	}
	if cfg.Width < 1 || cfg.Height < 1 {
		return nil, ErrNotImage
	}
	if cfg.Width > maxSide || cfg.Height > maxSide {
		return nil, ErrTooBig
	}
	var img image.Image
	switch kind {
	case "png":
		img, err = png.Decode(bytes.NewReader(b))
	case "jpeg":
		img, err = jpeg.Decode(bytes.NewReader(b))
	case "gif":
		img, err = gif.Decode(bytes.NewReader(b)) // the first frame
	case "webp":
		img, err = webp.Decode(bytes.NewReader(b))
	}
	if err != nil {
		return nil, fmt.Errorf("the %s image could not be read: %w", strings.ToUpper(kind), err)
	}
	return img, nil
}

// fromRaster scales a decoded image to fit storeSide, centres it on a
// transparent square and encodes it as PNG.
func fromRaster(src image.Image) (*Image, error) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 1 || h < 1 {
		return nil, ErrNotImage
	}
	dst := square(src, storeSide, false)
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return &Image{MIME: mimePNG, Data: buf.Bytes(), Width: w, Height: h, Tone: toneOf(dst)}, nil
}

// square draws src centred on a transparent square: side wide when grow
// is set (smaller images are scaled up), else at most side.
func square(src image.Image, side int, grow bool) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	long := max(w, h)
	scale := 1.0
	if long > side || grow {
		scale = float64(side) / float64(long)
	}
	sw, sh := max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
	s := max(sw, sh)
	dst := image.NewNRGBA(image.Rect(0, 0, s, s))
	at := image.Rect((s-sw)/2, (s-sh)/2, (s-sw)/2+sw, (s-sh)/2+sh)
	if sw == w && sh == h {
		draw.Draw(dst, at, src, b.Min, draw.Src)
	} else {
		draw.CatmullRom.Scale(dst, at, src, b, draw.Src, nil)
	}
	return dst
}

// toneOf says whether an icon on a transparent ground is dark or light,
// from its visible pixels' mean luminance. Opaque icons carry their own
// ground and need nothing.
func toneOf(img *image.NRGBA) string {
	var n, clear int
	var sum float64
	for i := 0; i+3 < len(img.Pix); i += 4 {
		a := img.Pix[i+3]
		if a < 26 {
			clear++
			continue
		}
		if a < 128 {
			continue
		}
		n++
		sum += luminance(img.Pix[i], img.Pix[i+1], img.Pix[i+2])
	}
	total := len(img.Pix) / 4
	if n == 0 || clear*10 < total { // under a tenth see-through: it brings its own ground
		return ""
	}
	switch mean := sum / float64(n); {
	case mean < 0.08:
		return "dark"
	case mean > 0.7:
		return "light"
	}
	return ""
}

var linear = sync.OnceValue(func() (t [256]float64) {
	for i := range t {
		c := float64(i) / 255
		if c <= 0.04045 {
			t[i] = c / 12.92
		} else {
			t[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return t
})

// luminance is the WCAG relative luminance of an sRGB colour.
func luminance(r, g, b uint8) float64 {
	t := linear()
	return 0.2126*t[r] + 0.7152*t[g] + 0.0722*t[b]
}

// EmailPNG renders a stored PNG at EmailSide square, for email and other
// places that want one fixed size.
func EmailPNG(stored []byte) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(stored))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, square(src, EmailSide, true)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- ICO ----

// decodeICO reads a Windows icon: it picks the largest image inside (PNG
// or an uncompressed bitmap) that decodes.
func decodeICO(b []byte) (image.Image, error) {
	n := int(binary.LittleEndian.Uint16(b[4:6]))
	if n == 0 || n > 64 || len(b) < 6+16*n {
		return nil, errors.New("the ICO file is damaged")
	}
	type entry struct{ w, h, bpp, off, size int }
	var es []entry
	for i := range n {
		e := b[6+16*i:]
		w, h := int(e[0]), int(e[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		size, off := int(binary.LittleEndian.Uint32(e[8:12])), int(binary.LittleEndian.Uint32(e[12:16]))
		if off < 6+16*n || size <= 0 || size > len(b) || off > len(b)-size {
			continue
		}
		es = append(es, entry{w, h, int(binary.LittleEndian.Uint16(e[6:8])), off, size})
	}
	// Largest first; at one size, the most colours.
	for i := 1; i < len(es); i++ {
		for j := i; j > 0 && (es[j].w*es[j].h > es[j-1].w*es[j-1].h || (es[j].w*es[j].h == es[j-1].w*es[j-1].h && es[j].bpp > es[j-1].bpp)); j-- {
			es[j], es[j-1] = es[j-1], es[j]
		}
	}
	for _, e := range es {
		data := b[e.off : e.off+e.size]
		if bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			if img, err := decodeRaster("png", data); err == nil {
				return img, nil
			}
			continue
		}
		if img, err := decodeDIB(data); err == nil {
			return img, nil
		}
	}
	return nil, errors.New("the ICO file has no image the box can read")
}

// decodeDIB reads an icon's bitmap: a BITMAPINFOHEADER, a palette for 8
// bits and fewer, the colour rows bottom-up, then a 1-bit transparency mask.
func decodeDIB(d []byte) (image.Image, error) {
	bad := errors.New("unsupported icon bitmap")
	if len(d) < 40 {
		return nil, bad
	}
	hdr := int(binary.LittleEndian.Uint32(d[0:4]))
	w := int(int32(binary.LittleEndian.Uint32(d[4:8])))
	h := int(int32(binary.LittleEndian.Uint32(d[8:12]))) / 2
	bpp := int(binary.LittleEndian.Uint16(d[14:16]))
	comp := binary.LittleEndian.Uint32(d[16:20])
	used := int(binary.LittleEndian.Uint32(d[32:36]))
	if hdr < 40 || hdr > len(d) || w < 1 || h < 1 || w > 256 || h > 256 || (comp != 0 && !(comp == 3 && bpp == 32)) {
		return nil, bad
	}
	var pal []color.NRGBA
	switch bpp {
	case 1, 4, 8:
		n := used
		if n == 0 || n > 1<<bpp {
			n = 1 << bpp
		}
		if len(d) < hdr+4*n {
			return nil, bad
		}
		for i := range n {
			p := d[hdr+4*i:]
			pal = append(pal, color.NRGBA{p[2], p[1], p[0], 255})
		}
	case 24, 32:
	default:
		return nil, bad
	}
	start := hdr + 4*len(pal)
	stride := (w*bpp + 31) / 32 * 4
	mstride := (w + 31) / 32 * 4
	if len(d) < start+stride*h {
		return nil, bad
	}
	mask := d[start+stride*h:]
	hasMask := len(mask) >= mstride*h
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	anyAlpha := false
	for y := range h {
		row := d[start+y*stride:]
		oy := h - 1 - y
		for x := range w {
			var c color.NRGBA
			switch bpp {
			case 32:
				c = color.NRGBA{row[4*x+2], row[4*x+1], row[4*x], row[4*x+3]}
				anyAlpha = anyAlpha || c.A != 0
			case 24:
				c = color.NRGBA{row[3*x+2], row[3*x+1], row[3*x], 255}
			default:
				var idx int
				switch bpp {
				case 8:
					idx = int(row[x])
				case 4:
					idx = int(row[x/2]>>(4*(1-x%2))) & 0xf
				case 1:
					idx = int(row[x/8]>>(7-x%8)) & 1
				}
				if idx < len(pal) {
					c = pal[idx]
				}
			}
			img.SetNRGBA(x, oy, c)
		}
	}
	if (bpp != 32 || !anyAlpha) && hasMask {
		// No alpha channel: the AND mask marks transparent pixels.
		for y := range h {
			row := mask[y*mstride:]
			oy := h - 1 - y
			for x := range w {
				i := img.PixOffset(x, oy)
				if row[x/8]>>(7-x%8)&1 == 1 {
					img.Pix[i+3] = 0
				} else {
					img.Pix[i+3] = 255
				}
			}
		}
	}
	return img, nil
}
