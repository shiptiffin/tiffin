package email

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Parsing each provider's event payloads into Events. Field names follow
// the providers' docs:
//   - SendGrid: https://www.twilio.com/docs/sendgrid/for-developers/tracking-events/event
//   - Resend:   https://resend.com/docs/webhooks/event-types
//   - Postmark: https://postmarkapp.com/developer/webhooks/webhooks-overview

// tiffinArg is the SendGrid unique argument (and Postmark metadata key)
// that carries our message ID.
const tiffinArg = "tiffin_id"

// postmarkMetaKey is the metadata key Postmark reports for the
// X-PM-Metadata-tiffin-id header.
const postmarkMetaKey = "tiffin-id"

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func digest(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:12])
}

// normMessageID gives a Message-ID one form: "<local@host>".
func normMessageID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return "<" + strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">") + ">"
}

// ---- SendGrid ----

type sgEvent map[string]any

func (e sgEvent) str(k string) string {
	switch v := e[k].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	}
	return ""
}

// parseSendGrid reads a SendGrid Event Webhook POST: a JSON array of events.
// Our message ID comes back as the tiffin_id unique argument (a top-level
// field), and smtp-id is the Message-ID header.
func parseSendGrid(body []byte) ([]inEvent, error) {
	var raw []sgEvent
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("not a JSON array of events")
	}
	out := make([]inEvent, 0, len(raw))
	for _, e := range raw {
		ev := inEvent{Event: Event{Provider: ProviderSendGrid, Recipient: normAddr(e.str("email"))}}
		if ts, ok := e["timestamp"].(float64); ok && ts > 0 {
			ev.At = time.Unix(int64(ts), 0).UTC()
		}
		reason := clip(firstOf(e.str("reason"), e.str("response")), 500)
		if st := e.str("status"); st != "" && reason != "" && !strings.Contains(reason, st) {
			reason = st + " " + reason
		}
		switch e.str("event") {
		case "delivered":
			ev.Type, ev.Detail = EventDelivered, clip(e.str("response"), 300)
		case "deferred":
			ev.Type, ev.Detail = EventDeferred, reason
			if a := e.str("attempt"); a != "" {
				ev.Detail = strings.TrimSpace("attempt " + a + ": " + reason)
			}
		case "bounce":
			// type "bounce" is a hard bounce, "blocked" a soft one.
			ev.Type, ev.Detail = EventBounced, reason
			ev.Hard = e.str("type") != "blocked"
			if c := e.str("bounce_classification"); c != "" {
				ev.Detail = strings.TrimSpace(c + ": " + reason)
			}
			if ev.Hard {
				ev.suppress = "bounce"
			}
		case "dropped":
			ev.Type, ev.Detail = EventDropped, reason
			switch strings.ToLower(e.str("reason")) {
			case "bounced address", "invalid":
				ev.suppress = "bounce"
			case "spam reporting address":
				ev.suppress = "complaint"
			case "unsubscribed address":
				ev.suppress = "unsubscribe"
			}
		case "spamreport":
			ev.Type, ev.suppress = EventComplained, "complaint"
		case "unsubscribe", "group_unsubscribe":
			ev.Type, ev.suppress = EventUnsubscribed, "unsubscribe"
		case "open":
			ev.Type = EventOpened
		case "click":
			ev.Type, ev.Detail = EventClicked, clip(e.str("url"), 300)
		default:
			continue // processed, group_resubscribe, account_status_change...
		}
		ev.TiffinID = e.str(tiffinArg)
		ev.HeaderID = normMessageID(e.str("smtp-id"))
		if id := e.str("sg_message_id"); id != "" {
			ev.ProviderID, _, _ = strings.Cut(id, ".")
		}
		ev.Key = e.str("sg_event_id")
		if ev.Key == "" {
			b, _ := json.Marshal(e)
			ev.Key = "h:" + digest(string(b))
		}
		if ev.At.IsZero() {
			ev.At = time.Now().UTC()
		}
		out = append(out, ev)
	}
	return out, nil
}

func firstOf(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// ---- Resend ----

type resendPayload struct {
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
	Data      struct {
		EmailID   string   `json:"email_id"`
		MessageID string   `json:"message_id"`
		To        []string `json:"to"`
		Subject   string   `json:"subject"`
		Bounce    *struct {
			Type    string `json:"type"`
			SubType string `json:"subType"`
			Message string `json:"message"`
		} `json:"bounce"`
		Click *struct {
			Link string `json:"link"`
		} `json:"click"`
		Failed *struct {
			Reason string `json:"reason"`
		} `json:"failed"`
		Suppressed *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"suppressed"`
	} `json:"data"`
}

// parseResend reads one Resend event. The message is matched by its
// Message-ID header (data.message_id), then Resend's email_id once known.
func parseResend(body []byte, deliveryID string) ([]inEvent, error) {
	var p resendPayload
	if err := json.Unmarshal(body, &p); err != nil || p.Type == "" {
		return nil, errors.New("not a Resend event")
	}
	ev := inEvent{Event: Event{Provider: ProviderResend, At: p.CreatedAt.UTC()}, HeaderID: normMessageID(p.Data.MessageID),
		ProviderID: p.Data.EmailID, Subject: p.Data.Subject}
	switch p.Type {
	case "email.delivered":
		ev.Type = EventDelivered
	case "email.delivery_delayed":
		ev.Type = EventDeferred
	case "email.bounced":
		ev.Type, ev.Hard = EventBounced, true
		if b := p.Data.Bounce; b != nil {
			ev.Hard = !strings.EqualFold(b.Type, "Temporary") && !strings.EqualFold(b.Type, "Transient")
			ev.Detail = clip(strings.TrimSpace(b.SubType+": "+b.Message), 500)
		}
		if ev.Hard {
			ev.suppress = "bounce"
		}
	case "email.complained":
		ev.Type, ev.suppress = EventComplained, "complaint"
	case "email.opened":
		ev.Type = EventOpened
	case "email.clicked":
		ev.Type = EventClicked
		if p.Data.Click != nil {
			ev.Detail = clip(p.Data.Click.Link, 300)
		}
	case "email.failed":
		ev.Type = EventFailed
		if p.Data.Failed != nil {
			ev.Detail = strings.ReplaceAll(p.Data.Failed.Reason, "_", " ")
		}
	case "email.suppressed":
		ev.Type, ev.suppress = EventDropped, "bounce"
		if s := p.Data.Suppressed; s != nil {
			ev.Detail = clip(s.Message, 500)
		}
	default:
		return nil, nil // sent, scheduled, received, domain and contact events
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	if deliveryID == "" {
		deliveryID = "h:" + digest(string(body))
	}
	out := []inEvent{}
	for _, to := range p.Data.To {
		e := ev
		e.Recipient = normAddr(to)
		e.Key = deliveryID + "/" + e.Recipient
		out = append(out, e)
	}
	return out, nil
}

// ---- Postmark ----

type postmarkPayload struct {
	RecordType   string            `json:"RecordType"`
	ID           json.Number       `json:"ID"`
	Type         string            `json:"Type"`
	TypeCode     int               `json:"TypeCode"`
	MessageID    string            `json:"MessageID"`
	Recipient    string            `json:"Recipient"`
	Email        string            `json:"Email"`
	Description  string            `json:"Description"`
	Details      string            `json:"Details"`
	Inactive     bool              `json:"Inactive"`
	Metadata     map[string]string `json:"Metadata"`
	DeliveredAt  time.Time         `json:"DeliveredAt"`
	BouncedAt    time.Time         `json:"BouncedAt"`
	ReceivedAt   time.Time         `json:"ReceivedAt"`
	ChangedAt    time.Time         `json:"ChangedAt"`
	OriginalLink string            `json:"OriginalLink"`
	Subject      string            `json:"Subject"`
	Suppress     bool              `json:"SuppressSending"`
	SuppressWhy  string            `json:"SuppressionReason"`
}

// parsePostmark reads one Postmark webhook. Our message ID comes back as
// the tiffin-id metadata field (from the X-PM-Metadata-tiffin-id header).
func parsePostmark(body []byte) ([]inEvent, error) {
	var p postmarkPayload
	d := json.NewDecoder(strings.NewReader(string(body)))
	d.UseNumber()
	if err := d.Decode(&p); err != nil || p.RecordType == "" {
		return nil, errors.New("not a Postmark webhook")
	}
	ev := inEvent{Event: Event{Provider: ProviderPostmark, Recipient: normAddr(firstOf(p.Recipient, p.Email))},
		ProviderID: p.MessageID, TiffinID: p.Metadata[postmarkMetaKey], Subject: p.Subject}
	at := p.DeliveredAt
	switch p.RecordType {
	case "Delivery":
		ev.Type, ev.Detail = EventDelivered, clip(p.Details, 300)
	case "Bounce":
		at = p.BouncedAt
		ev.Detail = clip(strings.TrimSpace(p.Description+" "+p.Details), 500)
		switch p.Type {
		case "Transient", "SoftBounce", "DnsError":
			ev.Type = EventDeferred
			if p.Type == "SoftBounce" {
				ev.Type = EventBounced
			}
		default:
			ev.Type = EventBounced
			ev.Hard = p.Inactive || p.Type == "HardBounce" || p.Type == "BadEmailAddress"
		}
		if ev.Hard {
			ev.suppress = "bounce"
		}
	case "SpamComplaint":
		at = p.BouncedAt
		ev.Type, ev.suppress = EventComplained, "complaint"
	case "Open":
		at = p.ReceivedAt
		ev.Type = EventOpened
	case "Click":
		at = p.ReceivedAt
		ev.Type, ev.Detail = EventClicked, clip(p.OriginalLink, 300)
	case "SubscriptionChange":
		at = p.ChangedAt
		if !p.Suppress {
			return nil, nil // reactivated: the owner decides in Tiffin
		}
		ev.Type, ev.Detail = EventUnsubscribed, p.SuppressWhy
		ev.suppress = "unsubscribe"
		if p.SuppressWhy == "HardBounce" {
			ev.suppress = "bounce"
		} else if p.SuppressWhy == "SpamComplaint" {
			ev.suppress = "complaint"
		}
	default:
		return nil, nil // Inbound and anything new
	}
	ev.At = at.UTC()
	if at.IsZero() {
		ev.At = time.Now().UTC()
	}
	ev.Key = p.RecordType + "/" + firstOf(p.ID.String(), digest(p.MessageID, ev.Recipient, ev.At.Format(time.RFC3339Nano)))
	return []inEvent{ev}, nil
}

// parseEvents dispatches by provider.
func parseEvents(provider string, body []byte, deliveryID string) ([]inEvent, error) {
	switch provider {
	case ProviderSendGrid:
		return parseSendGrid(body)
	case ProviderResend:
		return parseResend(body, deliveryID)
	case ProviderPostmark:
		return parsePostmark(body)
	}
	return nil, errors.New("unknown provider")
}
