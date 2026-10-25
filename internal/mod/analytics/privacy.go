package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/manifest"
)

// revenueConnected reports whether a project has a payment provider
// connected (revenue.go sets it); the privacy text then covers payments.
var revenueConnected = func(ctx context.Context, m *Module, project string) bool { return false }

// PrivacyView is the text a project's privacy policy should carry for its
// analytics settings.
type PrivacyView struct {
	Project  string   `json:"project"`
	Settings Settings `json:"settings"`
	Revenue  bool     `json:"revenue" doc:"A payment provider is connected: payments are linked to accounts"`
	Version  string   `json:"version" doc:"The text's version; History records it when attribution is turned on"`
	Hash     string   `json:"hash" doc:"First 12 hex digits of the text's SHA-256: it changes whenever the text does, so a page can tell its copy is out of date"`
	Markdown string   `json:"markdown" doc:"The text, in Markdown, to paste into the privacy policy"`
}

// privacyText is the privacy policy text for a project's analytics.
func privacyText(s Settings, revenue bool) string {
	var b strings.Builder
	b.WriteString("### Analytics\n\n")
	b.WriteString("We count visits to this site with first-party analytics that runs on our own server; nothing is sent to anyone else. " +
		"For each page view we keep the page's address without its query string, the name of the site that sent you (such as \"news.ycombinator.com\"), " +
		"your country, the kind of browser, system and device, and the time. Visitors are told apart by a code made from the site, your IP address and " +
		"browser string and a secret that changes every day and is deleted within 48 hours, so visits on different days can't be linked. " +
		"No IP addresses or browser strings are stored. Bots are not counted, and neither is anyone whose browser sends Global Privacy Control.\n\n")
	if !s.Attribution {
		b.WriteString("This analytics uses no cookies and stores nothing on your device.\n")
		return b.String()
	}
	b.WriteString("### Where our customers come from\n\n")
	var keep []string
	if s.Campaigns {
		keep = append(keep, "the campaign tags in the link you followed (utm_source, utm_medium, utm_campaign and ref)")
	}
	if s.Signups {
		keep = append(keep, "when you create an account, where the visit you signed up in came from (the site that sent you, those tags and the first page you saw), saved with your account")
	}
	if revenue {
		keep = append(keep, "when you pay, the payment's amount, currency and date, linked to your account or to the visit you paid in")
	}
	if len(keep) > 0 {
		b.WriteString("To see which sites and campaigns bring us customers, we also keep " + strings.Join(keep, "; ") + ". ")
	}
	b.WriteString("We use this only to understand our own marketing, never to show you ads or to track you on other sites, and we share it with no one. " +
		"Our legitimate interest in knowing what brings us customers is the legal basis (GDPR Article 6(1)(f)). " +
		"To object, turn on Global Privacy Control in your browser" + optOutSuffix(s) + "; you can also ask us to delete what is saved with your account. " +
		"It is deleted with your account, or when we stop collecting it.\n\n")
	switch s.FirstVisitCookie {
	case manifest.FirstVisitOutsideEU, manifest.FirstVisitAskEU:
		b.WriteString("### Cookies\n\n")
		b.WriteString("We set one first-party cookie, `tiffin_first_visit`, for 90 days. It holds a random code, no personal details, and lets us see " +
			"which site first sent you if you later sign up or buy. Only this site can read it; it is never shared or used across sites. ")
		if s.FirstVisitCookie == manifest.FirstVisitAskEU {
			b.WriteString("We don't set it for visitors in the EU, EEA or UK unless they agree, and you can change your mind at any time with the link below. ")
		} else {
			b.WriteString("We don't set it for visitors in the EU, EEA or UK. ")
		}
		b.WriteString("To opt out, turn on Global Privacy Control in your browser, or use [this link](/_tiffin/opt-out).\n")
	default:
		b.WriteString("This analytics uses no cookies and stores nothing on your device.\n")
	}
	return b.String()
}

func optOutSuffix(s Settings) string {
	if s.FirstVisitCookie == manifest.FirstVisitOutsideEU || s.FirstVisitCookie == manifest.FirstVisitAskEU {
		return " or use [this link](/_tiffin/opt-out)"
	}
	return ""
}

func (m *Module) privacy(ctx context.Context, project string) PrivacyView {
	s := m.Settings(ctx, project)
	rev := s.Attribution && revenueConnected(ctx, m, project)
	md := privacyText(s, rev)
	sum := sha256.Sum256([]byte(md))
	return PrivacyView{Project: project, Settings: s, Revenue: rev, Version: change.PrivacyTextVersion, Hash: hex.EncodeToString(sum[:])[:12], Markdown: md}
}

func (m *Module) registerPrivacyAPI(a huma.API) {
	huma.Register(a, api.Op("analytics-privacy", http.MethodGet, "/v1/analytics/privacy", "analytics privacy", api.RiskRead,
		"Show the privacy policy text for analytics",
		"The paragraphs a project's privacy policy should carry for its analytics settings (Basic counting, and marketing attribution, payments and the "+
			"first-visit cookie when they are on), in Markdown, with a version and a hash that changes whenever the text does. "+
			"Show it to the person when attribution is turned on or changed, and tell them to update their privacy policy.", "analytics"),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `query:"project" required:"true" doc:"Project"`
		}) (*struct{ Body PrivacyView }, error) {
			if err := m.ready(ctx, in.Project); err != nil {
				return nil, err
			}
			return &struct{ Body PrivacyView }{m.privacy(ctx, in.Project)}, nil
		}))
}
