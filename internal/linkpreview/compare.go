package linkpreview

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"github.com/shiptiffin/tiffin/internal/dnskit"
)

// Compare records what changed from an earlier report to r.
func (r *LinkReport) Compare(old *LinkReport, c *LinkBaseline) {
	r.Compared, r.Changes = nil, nil
	if old == nil || old.Status == StatusUnreadable || r.Status == StatusUnreadable {
		return
	}
	r.Compared = c
	field := func(name, from, to string) {
		if from != to {
			r.Changes = append(r.Changes, LinkChange{Field: name, From: from, To: to, Worse: to == "" && from != ""})
		}
	}
	field("Title", old.Title, r.Title)
	field("Description", old.Description, r.Description)
	oi, ni := "", ""
	if old.Image != nil {
		oi = old.Image.URL
	}
	if r.Image != nil {
		ni = r.Image.URL
	}
	field("Share image", oi, ni)
	if oi != "" && oi == ni && old.Image.Hash != "" && r.Image.Hash != "" && old.Image.Hash != r.Image.Hash {
		r.Changes = append(r.Changes, LinkChange{Field: "Share image", From: imageWords(old.Image), To: imageWords(r.Image) + ", new picture"})
	}
	field("X card", old.Card, r.Card)
	field("Canonical address", old.Canonical, r.Canonical)
	field("Site name", old.SiteName, r.SiteName)

	// Checks that got worse (or better) say what the fields don't.
	was := map[string]LinkCheck{}
	for _, c := range old.Checks {
		was[c.ID] = c
	}
	for _, c := range r.Checks {
		o, ok := was[c.ID]
		if !ok || o.Status == c.Status {
			continue
		}
		bad := worse(o.Status, c.Status) == c.Status
		listed := false
		for i := range r.Changes {
			if r.Changes[i].Field == c.Label {
				listed = true
				r.Changes[i].Worse = r.Changes[i].Worse || bad
			}
		}
		if !listed {
			r.Changes = append(r.Changes, LinkChange{Field: c.Label, From: o.Status, To: c.Status, Worse: bad})
		}
	}
}

func imageWords(im *ShareImage) string {
	if im.Width > 0 {
		return fmt.Sprintf("%d×%d", im.Width, im.Height)
	}
	return "an image"
}

// OutsideClient fetches images on other sites: public addresses only (not
// this computer, its networks or the box's own addresses), checked when
// connecting so a name can't resolve its way in.
func OutsideClient(self func() []netip.Addr) *http.Client {
	d := &net.Dialer{Timeout: fetchTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return err
		}
		ip := ap.Addr().Unmap()
		if !dnskit.IsPublic(ip) || ip.IsLoopback() {
			return errors.New("it resolves to an address that isn't public")
		}
		if self != nil {
			for _, s := range self() {
				if s.Unmap() == ip {
					return errors.New("it resolves to the box itself")
				}
			}
		}
		return nil
	}}
	return &http.Client{
		Transport: &http.Transport{DialContext: d.DialContext, Proxy: nil, TLSHandshakeTimeout: fetchTimeout,
			ResponseHeaderTimeout: fetchTimeout, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
