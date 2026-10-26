package change

import "encoding/json"

// PrivacyTextVersion names the privacy text the dashboard and
// `tiffin analytics privacy` show for attribution's settings; a turn-on step
// records it, so History says which text the person was shown.
const PrivacyTextVersion = "2026-10-25"

type attributionOf struct {
	On     bool
	Cookie string // off, outside-eu or ask-eu
}

func attribution(spec json.RawMessage) attributionOf {
	var s struct {
		Attribution *struct {
			FirstVisitCookie string `json:"firstVisitCookie"`
		} `json:"attribution"`
	}
	if spec == nil || json.Unmarshal(spec, &s) != nil || s.Attribution == nil {
		return attributionOf{Cookie: "off"}
	}
	c := s.Attribution.FirstVisitCookie
	if c == "" {
		c = "off"
	}
	return attributionOf{On: true, Cookie: c}
}

// cookieRank orders first-visit cookie modes by how much they set.
func cookieRank(c string) int {
	switch c {
	case "outside-eu":
		return 1
	case "ask-eu":
		return 2
	}
	return 0
}

// AttributionTurnedOn reports what plan turns on that only a person may
// turn on: "marketing attribution", "the first-visit cookie", or "" for
// neither. API keys are refused it (people in the dashboard and the owner
// token may): it links visits to people, and the project's privacy policy
// has to say so.
func AttributionTurnedOn(plan *Plan) string {
	if plan == nil {
		return ""
	}
	for _, op := range plan.Ops {
		if op.Address != KindService+"/analytics" || op.Action == Delete {
			continue
		}
		a, b := attribution(op.Before), attribution(op.After)
		if b.On && !a.On {
			return "marketing attribution"
		}
		if cookieRank(b.Cookie) > cookieRank(a.Cookie) {
			return "the first-visit cookie"
		}
	}
	return ""
}

// attributionReason is an analytics step's tier and reason when it turns
// attribution or its cookie on or off; ok is false otherwise.
func attributionReason(before, after json.RawMessage) (Tier, string, bool) {
	a, b := attribution(before), attribution(after)
	switch {
	case b.On && !a.On:
		return TierReversible, "turns on marketing attribution: campaign tags are kept and each new account's source is saved with it; " +
			"your privacy policy must say so (tiffin analytics privacy, text " + PrivacyTextVersion + "); undo turns it off and deletes what it collected", true
	case a.On && !b.On:
		return TierIrreversible, "turns off marketing attribution and deletes what it linked to accounts: sign-up sources, checkout returns, first visits and payments", true
	case cookieRank(b.Cookie) > cookieRank(a.Cookie):
		return TierReversible, "sets the first-visit cookie (" + b.Cookie + ") for 90 days on visitors' browsers; your privacy policy must list it " +
			"(tiffin analytics privacy, text " + PrivacyTextVersion + ")", true
	case cookieRank(b.Cookie) < cookieRank(a.Cookie):
		return TierReversible, "sets the first-visit cookie for fewer visitors (" + b.Cookie + "); first visits already remembered stay up to 90 days", true
	}
	return "", "", false
}
