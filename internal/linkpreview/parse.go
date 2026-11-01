package linkpreview

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// parsed is what a page says about itself.
type parsed struct {
	title string
	meta  map[string]string // name or property → its first value
	links map[string]string // rel → the first href
	icons []string          // icon hrefs, in order
	tags  []PageTag
}

func (p *parsed) first(name string) string { return p.meta[name] }
func (p *parsed) link(rel string) string   { return p.links[rel] }

// shared are the meta names worth listing (and reading).
func shared(name string) bool {
	switch name {
	case "description", "robots", "googlebot", "theme-color", "application-name":
		return true
	}
	return strings.HasPrefix(name, "og:") || strings.HasPrefix(name, "twitter:") || strings.HasPrefix(name, "article:") || strings.HasPrefix(name, "fb:")
}

// parse reads a page's title, meta and link tags. It reads the whole page,
// not only <head>: unfurlers read meta tags wherever they are, and some
// frameworks stream them into <body>. A <title> inside SVG is not the
// page's.
func parse(page []byte) *parsed {
	p := &parsed{meta: map[string]string{}, links: map[string]string{}}
	z := html.NewTokenizer(bytes.NewReader(page))
	svg := 0
	inTitle := false
	var title strings.Builder
	add := func(name, value string) {
		value = strings.TrimSpace(value)
		if len(p.tags) < maxTags {
			v := value
			if len(v) > maxValue {
				v = v[:maxValue] + "…"
			}
			p.tags = append(p.tags, PageTag{Name: name, Value: v})
		}
	}
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			p.title = strings.Join(strings.Fields(title.String()), " ")
			return p
		case html.TextToken:
			if inTitle {
				title.Write(z.Text())
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch atom.Lookup(name) {
			case atom.Svg:
				svg = max(0, svg-1)
			case atom.Title:
				if inTitle {
					inTitle = false
					add("title", strings.Join(strings.Fields(title.String()), " "))
				}
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			a := atom.Lookup(name)
			if a == atom.Svg && tt == html.StartTagToken {
				svg++
			}
			if svg > 0 {
				continue
			}
			switch a {
			case atom.Title:
				if p.title == "" && title.Len() == 0 && tt == html.StartTagToken {
					inTitle = true
				}
			case atom.Meta:
				if !hasAttr {
					continue
				}
				at := attrs(z)
				key := strings.ToLower(strings.TrimSpace(first(at["property"], at["name"])))
				if key == "" || !shared(key) {
					continue
				}
				val := at["content"]
				add(key, val)
				if _, seen := p.meta[key]; !seen && strings.TrimSpace(val) != "" {
					p.meta[key] = strings.TrimSpace(val)
				}
			case atom.Link:
				if !hasAttr {
					continue
				}
				at := attrs(z)
				href := strings.TrimSpace(at["href"])
				if href == "" {
					continue
				}
				for _, rel := range strings.Fields(strings.ToLower(at["rel"])) {
					switch rel {
					case "canonical", "icon", "apple-touch-icon", "manifest", "alternate":
					default:
						continue
					}
					if rel == "alternate" && at["hreflang"] == "" {
						continue
					}
					add("link "+rel, href)
					if _, seen := p.links[rel]; !seen {
						p.links[rel] = href
					}
					if rel == "icon" {
						p.icons = append(p.icons, href)
					}
				}
			}
		}
	}
}

func attrs(z *html.Tokenizer) map[string]string {
	at := map[string]string{}
	for {
		k, v, more := z.TagAttr()
		key := strings.ToLower(string(k))
		if _, seen := at[key]; !seen {
			at[key] = string(v)
		}
		if !more {
			return at
		}
	}
}
