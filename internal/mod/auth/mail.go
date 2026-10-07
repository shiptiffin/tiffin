package auth

import (
	"context"
	"encoding/json"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
)

// Email sign-in needs mail that reaches people. In production, until the
// box can send email (an SMTP relay: the customer's own mail service), the
// engine refuses email + password sign-up, magic links, one-time codes,
// password resets and verification mail with EMAIL_NOT_SET_UP, rather than
// let anyone sign up with an address they don't own. Passkeys and sign-in
// providers keep working. Previews and local boxes keep using the dev inbox.

// EmailNotSetUp is what people and plans are told.
const EmailNotSetUp = "This app can't send email yet: connect a mail service in Settings › Email."

// emailMethods are the sign-in methods that send mail.
var emailMethods = []string{manifest.AuthEmail, manifest.AuthMagicLink, manifest.AuthOTP}

// mailLeaves reports whether the project's production mail goes out
// through a relay.
func mailLeaves(ctx context.Context, p *platform.Platform, project string, hasEmail bool) bool {
	if !hasEmail {
		return false
	}
	for _, m := range platform.Modules() {
		if s, ok := m.(interface {
			WillSend(context.Context, *platform.Platform, string) (bool, error)
		}); ok && m.Name() == "email" {
			sends, err := s.WillSend(ctx, p, project)
			return err == nil && sends
		}
	}
	return false
}

// EmailBlocked reports whether production email sign-in is refused: the
// box isn't a local one and its mail doesn't leave it.
func EmailBlocked(ctx context.Context, p *platform.Platform, project string, hasEmail bool) bool {
	return p != nil && !platform.IsLocalDomain(p.Domain) && !mailLeaves(ctx, p, project, hasEmail)
}

// PlanWarnings: email sign-in turned on where the box can't send email.
func (*Module) PlanWarnings(ctx context.Context, p *platform.Platform, project string, _ *change.Plan, desired map[string]change.Resource) []string {
	r, ok := desired[change.KindService+"/auth"]
	if !ok {
		return nil
	}
	var a manifest.Auth
	if json.Unmarshal(r.Spec, &a) != nil {
		return nil
	}
	if len(a.Methods) == 0 {
		a.Methods = manifest.DefaultAuthMethods
	}
	uses := false
	for _, m := range emailMethods {
		uses = uses || contains(a.Methods, m)
	}
	_, hasEmail := desired[change.KindService+"/email"]
	// Without an email service the manifest's own warning says to add one;
	// this one is about the box, and would wrongly say it has no relay.
	if !uses || !hasEmail || !EmailBlocked(ctx, p, project, hasEmail) {
		return nil
	}
	return []string{"services.auth: this box can't send email yet, so in production email + password sign-up, magic links, one-time codes and password resets " +
		"are refused (EMAIL_NOT_SET_UP) until you connect a mail service in Settings › Email (tiffin email relay set). Passkeys and sign-in providers work; " +
		"previews keep using the dev inbox"}
}
