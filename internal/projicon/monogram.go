package projicon

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// The fallback icon: a project's initials on a tint of its enamel. The
// dashboard draws it itself (crisp, in its own font, in both themes); the
// box renders the same tile as a PNG for email, where it can't.

// Letters are a project's initials: the first letters of its first two
// words ("my-shop" → "MS"), or of its name ("shop" → "S").
func Letters(project string) string {
	var out []rune
	for _, w := range strings.FieldsFunc(project, func(r rune) bool { return r == '-' || r == '_' || r == ' ' || r == '.' }) {
		for _, r := range w {
			out = append(out, unicode.ToUpper(r))
			break
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

// Enamel hues and chroma, as in the dashboard's tokens.css (light theme:
// email is read on white more often than not).
var enamels = map[string][3]float64{
	"leaf":     {0.6, 0.1, 150},
	"teal":     {0.6, 0.09, 200},
	"indigo":   {0.6, 0.11, 258},
	"plum":     {0.6, 0.12, 0},
	"chilli":   {0.6, 0.13, 38},
	"turmeric": {0.6, 0.11, 90},
}

// The light theme's raised paper and ink, which the tile is mixed from
// (the same mix as the dashboard's monogram, in OKLab).
var (
	paperRaised = [3]float64{0.993, 0.004, 85}
	ink         = [3]float64{0.235, 0.014, 60}
)

// Tile shares of the enamel: the ground is mostly paper, the letters mostly
// enamel deepened with ink. Both keep ≥ 4.5:1 for every enamel (tested).
const (
	groundShare = 0.24
	letterShare = 0.55
)

// MonogramColors are the tile's ground and letter colours for an enamel.
func MonogramColors(enamel string) (ground, letters color.NRGBA) {
	e, ok := enamels[enamel]
	if !ok {
		e = enamels["indigo"]
	}
	return srgb(mixOKLab(e, paperRaised, groundShare)), srgb(mixOKLab(e, ink, letterShare))
}

// EmailAccent is an enamel as an email button colour, "#rrggbb": the
// monogram's letter colour (enamel deepened with ink), which keeps ≥ 4.5:1
// against white text and a light page.
func EmailAccent(enamel string) string {
	_, c := MonogramColors(enamel)
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

// mixOKLab mixes share of a with the rest of b, like CSS
// color-mix(in oklab, a share, b). Returns OKLab.
func mixOKLab(a, b [3]float64, share float64) [3]float64 {
	la, lb := oklchToLab(a), oklchToLab(b)
	var m [3]float64
	for i := range m {
		m[i] = la[i]*share + lb[i]*(1-share)
	}
	return m
}

func oklchToLab(c [3]float64) [3]float64 {
	h := c[2] * math.Pi / 180
	return [3]float64{c[0], c[1] * math.Cos(h), c[1] * math.Sin(h)}
}

// srgb converts OKLab to 8-bit sRGB.
func srgb(lab [3]float64) color.NRGBA {
	L, a, b := lab[0], lab[1], lab[2]
	l := math.Pow(L+0.3963377774*a+0.2158037573*b, 3)
	m := math.Pow(L-0.1055613458*a-0.0638541728*b, 3)
	s := math.Pow(L-0.0894841775*a-1.291485548*b, 3)
	lin := [3]float64{
		4.0767416621*l - 3.3077115913*m + 0.2309699292*s,
		-1.2684380046*l + 2.6097574011*m - 0.3413193965*s,
		-0.0041960863*l - 0.7034186147*m + 1.707614701*s,
	}
	var out [3]uint8
	for i, x := range lin {
		x = math.Min(1, math.Max(0, x))
		if x <= 0.0031308 {
			x *= 12.92
		} else {
			x = 1.055*math.Pow(x, 1/2.4) - 0.055
		}
		out[i] = uint8(math.Round(x * 255))
	}
	return color.NRGBA{out[0], out[1], out[2], 255}
}

// Contrast is the WCAG 2 contrast ratio of two opaque colours.
func Contrast(a, b color.NRGBA) float64 {
	la, lb := luminance(a.R, a.G, a.B), luminance(b.R, b.G, b.B)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

var boldFont = sync.OnceValues(func() (*opentype.Font, error) { return opentype.Parse(gobold.TTF) })

// MonogramPNG draws the fallback tile at side pixels: a rounded square
// in the enamel's ground with the initials centred on it.
func MonogramPNG(project, enamel string, side int) ([]byte, error) {
	ground, ink := MonogramColors(enamel)
	img := image.NewNRGBA(image.Rect(0, 0, side, side))
	// The rounded square, anti-aliased.
	s := float32(side)
	r := s * 0.22
	k := r * 0.4477 // control-point inset for a circular quarter (1 - 0.5523)
	z := vector.NewRasterizer(side, side)
	z.MoveTo(r, 0)
	z.LineTo(s-r, 0)
	z.CubeTo(s-k, 0, s, k, s, r)
	z.LineTo(s, s-r)
	z.CubeTo(s, s-k, s-k, s, s-r, s)
	z.LineTo(r, s)
	z.CubeTo(k, s, 0, s-k, 0, s-r)
	z.LineTo(0, r)
	z.CubeTo(0, k, k, 0, r, 0)
	z.ClosePath()
	z.Draw(img, img.Bounds(), image.NewUniform(ground), image.Point{})

	f, err := boldFont()
	if err != nil {
		return nil, err
	}
	text := Letters(project)
	size := float64(side) * 0.46
	if len([]rune(text)) > 1 {
		size = float64(side) * 0.38
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil, err
	}
	defer face.Close()
	d := &font.Drawer{Dst: img, Src: image.NewUniform(ink), Face: face}
	// Centre the letters' ink, not their advance box.
	bounds, _ := d.BoundString(text)
	bw := (bounds.Max.X - bounds.Min.X).Ceil()
	bh := (bounds.Max.Y - bounds.Min.Y).Ceil()
	x := (side-bw)/2 - bounds.Min.X.Floor()
	y := (side-bh)/2 - bounds.Min.Y.Floor()
	d.Dot = fixed.P(x, y)
	d.DrawString(text)

	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
