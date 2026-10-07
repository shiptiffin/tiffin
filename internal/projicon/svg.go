package projicon

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
)

// The SVG safety pass. An icon is drawn in an <img>, where scripts never
// run, but its URL can be opened on its own, so the stored file must be
// harmless there too. It is rebuilt from an allow-list: drawing elements
// and their presentation attributes only. Scripts, event handlers,
// foreignObject, embedded images, animation (which can rewrite links),
// links to anything but the file's own #ids, and CSS that can fetch
// (@import, url() other than #ids) are all dropped. Entities are refused.

const svgNS = "http://www.w3.org/2000/svg"
const xlinkNS = "http://www.w3.org/1999/xlink"

var svgElements = set(
	"svg", "g", "defs", "symbol", "use", "path", "rect", "circle", "ellipse", "line", "polyline", "polygon",
	"text", "tspan", "title", "desc", "linearGradient", "radialGradient", "stop", "clipPath", "mask", "pattern",
	"filter", "feGaussianBlur", "feOffset", "feBlend", "feColorMatrix", "feComposite", "feFlood", "feMerge",
	"feMergeNode", "feMorphology", "feDropShadow", "style",
)

var svgAttrs = set(
	"id", "class", "style", "viewBox", "width", "height", "x", "y", "x1", "x2", "y1", "y2", "cx", "cy", "r", "rx", "ry",
	"fx", "fy", "fr", "d", "points", "pathLength", "transform", "fill", "fill-opacity", "fill-rule", "stroke", "stroke-width",
	"stroke-linecap", "stroke-linejoin", "stroke-miterlimit", "stroke-dasharray", "stroke-dashoffset", "stroke-opacity",
	"opacity", "clip-path", "clip-rule", "mask", "offset", "stop-color", "stop-opacity", "gradientUnits", "gradientTransform",
	"spreadMethod", "preserveAspectRatio", "patternUnits", "patternContentUnits", "patternTransform", "clipPathUnits",
	"maskUnits", "maskContentUnits", "filter", "filterUnits", "primitiveUnits", "in", "in2", "result", "stdDeviation",
	"dx", "dy", "mode", "values", "type", "operator", "k1", "k2", "k3", "k4", "radius", "flood-color", "flood-opacity",
	"font-family", "font-size", "font-weight", "font-style", "text-anchor", "dominant-baseline", "letter-spacing",
	"visibility", "display", "color", "href", "shape-rendering", "vector-effect", "paint-order", "isolation",
	"mix-blend-mode", "color-interpolation-filters", "media",
)

func set(s ...string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

// looksSVG reports whether b starts like an SVG document: optional BOM,
// XML declaration, comments and doctype, then an <svg> root.
func looksSVG(b []byte) bool {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	b = bytes.TrimLeft(b, " \t\r\n")
	if len(b) == 0 || b[0] != '<' {
		return false
	}
	dec := xml.NewDecoder(io.LimitReader(bytes.NewReader(b), 8<<10))
	for {
		t, err := dec.RawToken()
		if err != nil {
			return false
		}
		switch t := t.(type) {
		case xml.StartElement:
			return t.Name.Local == "svg"
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return false
			}
		}
	}
}

// SanitizeSVG rebuilds an SVG from the allow-list and returns it with its
// size (from the viewBox, else width and height).
func SanitizeSVG(in []byte) ([]byte, int, int, error) {
	if len(in) > MaxBytes {
		return nil, 0, 0, ErrTooLarge
	}
	dec := xml.NewDecoder(bytes.NewReader(in))
	dec.Strict = true
	var out bytes.Buffer
	var stack []string // open elements written
	skip := 0          // depth inside a dropped element
	inStyle := false
	var style strings.Builder
	w, h := 0, 0
	root := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, 0, errors.New("the SVG is not well-formed XML")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			name := t.Name.Local
			if !root {
				if name != "svg" || (t.Name.Space != "" && t.Name.Space != svgNS) {
					return nil, 0, 0, errors.New("the file is not an SVG image")
				}
				root = true
			}
			if (t.Name.Space != "" && t.Name.Space != svgNS) || !svgElements[name] {
				skip = 1
				continue
			}
			out.WriteByte('<')
			out.WriteString(name)
			if len(stack) == 0 {
				out.WriteString(` xmlns="` + svgNS + `"`)
				w, h = svgSize(t.Attr)
			}
			for _, a := range t.Attr {
				an := a.Name.Local
				switch a.Name.Space {
				case "":
				case xlinkNS:
					if an != "href" {
						continue
					}
				default:
					continue
				}
				if !svgAttrs[an] || !safeValue(an, a.Value) {
					continue
				}
				out.WriteByte(' ')
				out.WriteString(an)
				out.WriteString(`="`)
				_ = xml.EscapeText(&out, []byte(a.Value))
				out.WriteByte('"')
			}
			out.WriteByte('>')
			stack = append(stack, name)
			if name == "style" {
				inStyle = true
				style.Reset()
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			if len(stack) == 0 {
				continue
			}
			name := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if name == "style" {
				inStyle = false
				if css := style.String(); safeCSS(css) {
					_ = xml.EscapeText(&out, []byte(css))
				}
			}
			out.WriteString("</" + name + ">")
		case xml.CharData:
			if skip > 0 || len(stack) == 0 {
				continue
			}
			if inStyle {
				style.Write(t)
				continue
			}
			_ = xml.EscapeText(&out, t)
		case xml.Directive:
			if bytes.Contains(bytes.ToUpper(t), []byte("ENTITY")) {
				return nil, 0, 0, errors.New("the SVG declares entities, which the box doesn't accept")
			}
		}
	}
	if !root || len(stack) != 0 {
		return nil, 0, 0, errors.New("the file is not an SVG image")
	}
	if out.Len() > MaxBytes {
		return nil, 0, 0, ErrTooLarge
	}
	return out.Bytes(), w, h, nil
}

// safeValue keeps an attribute only when it can't reach outside the file
// or run anything.
func safeValue(name, v string) bool {
	l := strings.ToLower(v)
	if strings.Contains(l, "javascript:") || strings.Contains(l, "data:") || strings.Contains(l, "\\") {
		return false
	}
	switch name {
	case "href":
		return strings.HasPrefix(strings.TrimSpace(v), "#")
	case "style":
		return safeCSS(v)
	}
	return localURLs(l)
}

// safeCSS allows a style sheet or style attribute that can't fetch
// anything: no @import, no escapes (which could spell url), and url()
// only to #ids in the file.
func safeCSS(css string) bool {
	l := strings.ToLower(css)
	for _, bad := range []string{"@import", "\\", "expression", "javascript:", "behavior", "binding", "image-set", "image(", "src(", "data:", "@font-face"} {
		if strings.Contains(l, bad) {
			return false
		}
	}
	return localURLs(l)
}

// localURLs reports whether every url(...) in s points at a #id.
func localURLs(s string) bool {
	for {
		i := strings.Index(s, "url(")
		if i < 0 {
			return true
		}
		rest := strings.TrimLeft(s[i+4:], " \t\r\n\"'")
		if !strings.HasPrefix(rest, "#") {
			return false
		}
		s = rest
	}
}

// svgSize reads an SVG's size from its root: the viewBox, else width and
// height in plain numbers or px.
func svgSize(attrs []xml.Attr) (int, int) {
	var w, h float64
	for _, a := range attrs {
		switch a.Name.Local {
		case "viewBox":
			f := strings.FieldsFunc(a.Value, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' })
			if len(f) == 4 {
				vw, e1 := strconv.ParseFloat(f[2], 64)
				vh, e2 := strconv.ParseFloat(f[3], 64)
				if e1 == nil && e2 == nil && vw > 0 && vh > 0 {
					return int(math.Ceil(vw)), int(math.Ceil(vh))
				}
			}
		case "width":
			w, _ = strconv.ParseFloat(strings.TrimSuffix(a.Value, "px"), 64)
		case "height":
			h, _ = strconv.ParseFloat(strings.TrimSuffix(a.Value, "px"), 64)
		}
	}
	if w > 0 && h > 0 {
		return int(math.Ceil(w)), int(math.Ceil(h))
	}
	return 0, 0
}
