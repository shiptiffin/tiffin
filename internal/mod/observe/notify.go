package observe

import "context"

// Notify implements platform.Notifier: a one-off notice from another
// module (a failed Postgres update) goes through the alert channels and
// into the alert history, under subject as the rule name.
func (m *Module) Notify(ctx context.Context, subject, summary string) {
	if !m.started.Load() || m.alerter == nil {
		return
	}
	m.alerter.notify(ctx, Rule{Name: subject, Kind: "notice", Enabled: true}, finding{Subject: subject, Summary: summary, Bad: true}, "firing")
}
