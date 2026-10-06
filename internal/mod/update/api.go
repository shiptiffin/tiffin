package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/release"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/btahir/tiffin/internal/version"
	"github.com/danielgtaylor/huma/v2"
)

// Status is how the box keeps Tiffin up to date.
type Status struct {
	Version    string     `json:"version" doc:"The Tiffin version running"`
	Release    bool       `json:"release" doc:"It is a release build; a development build updates with tiffin up only"`
	Auto       bool       `json:"auto" doc:"New releases install by themselves in the maintenance window"`
	Channel    string     `json:"channel" enum:"stable,edge"`
	Source     string     `json:"source" doc:"Where the box reads the channel's signed manifest ({channel} is replaced)"`
	Window     string     `json:"window,omitempty" doc:"When updates may install (HH:MM, server time; they start 30 minutes in). Empty: none, so updates wait for update apply"`
	NextRun    *time.Time `json:"nextRun,omitempty" doc:"When the window next opens for an update"`
	CheckedAt  *time.Time `json:"checkedAt,omitempty" doc:"When the box last read the manifest"`
	Available  *Available `json:"available,omitempty" doc:"A newer release of the channel"`
	CheckError string     `json:"checkError,omitempty" doc:"Why the last check failed"`
	Running    *Update    `json:"running,omitempty" doc:"The update in progress"`
	Updates    []Update   `json:"updates" doc:"Recent updates, newest first"`
	Summary    string     `json:"summary"`
}

func status(r record, now time.Time) *Status {
	_, err := release.ParseVersion(version.Version)
	s := &Status{Version: version.Version, Release: err == nil, Auto: !r.Manual, Channel: r.channel(), Source: r.source(),
		Window: window(r), Available: r.Available, CheckError: r.CheckError, Running: running(r), Updates: r.Updates}
	if s.Updates == nil {
		s.Updates = []Update{}
	}
	if !r.CheckedAt.IsZero() {
		s.CheckedAt = &r.CheckedAt
	}
	if s.Window != "" {
		n := nextRun(now, s.Window)
		s.NextRun = &n
	}
	s.Summary = s.words()
	return s
}

// words says where updates stand in a sentence or two.
func (s *Status) words() string {
	a := s.Available
	switch {
	case s.Running != nil:
		return s.Running.Summary
	case !s.Release:
		return "Tiffin " + s.Version + " is a development build: it updates with tiffin up"
	case s.CheckError != "":
		return "the last check failed: " + s.CheckError
	case a == nil && s.CheckedAt == nil:
		return "Tiffin " + s.Version + "; not checked for updates yet"
	case a == nil:
		return "Tiffin " + s.Version + " is up to date (checked " + s.CheckedAt.Local().Format("2006-01-02 15:04") + ")"
	case a.Blocked != "":
		return "Tiffin " + a.Version + " is out, but " + a.Blocked
	case !s.Auto:
		return "Tiffin " + a.Version + " is out; automatic updates are off: run tiffin update apply"
	case s.NextRun == nil:
		return "Tiffin " + a.Version + " is out; there is no maintenance window, so run tiffin update apply (or set one: tiffin update settings --window 04:00)"
	case !a.InRollout:
		return fmt.Sprintf("Tiffin %s is out to %d%% of boxes; this one gets it as the rollout widens (or now with tiffin update apply)", a.Version, a.Rollout)
	}
	return "Tiffin " + a.Version + " installs at " + s.NextRun.Local().Format("2006-01-02 15:04") + " (maintenance window), or now with tiffin update apply"
}

// Checks reports a failed update or a refused release.
func (*Module) Checks(context.Context, *platform.Platform) []platform.Check {
	if mod.updater() == nil {
		return nil
	}
	r := load()
	c := platform.Check{Name: "tiffin-updates", OK: true}
	s := status(r, time.Now())
	c.Detail = s.Summary
	if len(r.Updates) > 0 && r.Updates[0].Status != "ok" && r.Updates[0].Status != "running" {
		c.OK, c.Detail = false, r.Updates[0].Summary+"; "+s.Summary
	}
	if r.Refused {
		c.OK = false // a release that does not check out is worth a look; the network is not
	}
	return []platform.Check{c}
}

type settingsBody struct {
	Auto    *bool  `json:"auto,omitempty" doc:"Install new releases by themselves in the maintenance window (true), or only with update apply (false)"`
	Channel string `json:"channel,omitempty" enum:"stable,edge" doc:"stable: releases; edge: pre-releases too"`
	Source  string `json:"source,omitempty" doc:"Where to read the channel's signed manifest, {channel} replaced; default resets it. Releases must still be signed by a key this build trusts."`
	Window  string `json:"window,omitempty" doc:"When updates may install (HH:MM, server time); box resets it to the box's maintenance window (tiffin up --reboot-window)"`
}

// RegisterAPI adds the update operations.
func (*Module) RegisterAPI(a huma.API, p *platform.Platform) {
	const tag = "system"
	admin := func(ctx context.Context) (*updater, error) {
		if !api.PrincipalFrom(ctx).BoxAdmin() {
			return nil, fmt.Errorf("%w: Tiffin updates need a key with full access to all projects", tokens.ErrForbidden)
		}
		u := mod.updater()
		if u == nil {
			return nil, api.NewProblem(501, "internal", "Tiffin updates itself on a box; this server runs without --box")
		}
		u.collect(time.Now())
		return u, nil
	}
	type out struct{ Body *Status }

	huma.Register(a, api.Op("update-status", http.MethodGet, "/v1/box/update", "update status", api.RiskRead,
		"Show Tiffin updates",
		"The Tiffin version running, the channel it follows, whether new releases install by themselves, the maintenance window, "+
			"a newer release if there is one (and whether this box is in its rollout yet) and recent updates. Box admins only.", tag),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*out, error) {
			if _, err := admin(ctx); err != nil {
				return nil, err
			}
			return &out{status(load(), time.Now())}, nil
		}))

	huma.Register(a, api.Op("update-check", http.MethodPost, "/v1/box/update/check", "update check", api.RiskRead,
		"Check for a Tiffin update now",
		"Reads the channel's release manifest now and checks its signature against the keys this build trusts (the box also checks "+
			"once a day). Installs nothing. Box admins only.", tag),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*out, error) {
			u, err := admin(ctx)
			if err != nil {
				return nil, err
			}
			if _, _, err := u.checkNow(ctx); errors.Is(err, errBusy) {
				return nil, api.NewProblem(409, "conflict", err.Error())
			}
			return &out{status(load(), time.Now())}, nil
		}))

	apply := api.Op("update-apply", http.MethodPost, "/v1/box/update/apply", "update apply", api.RiskWrite,
		"Install the newest Tiffin release now",
		"Installs the channel's newest release now, whatever the window or rollout: checks the manifest's signature, downloads the "+
			"build and checks its sha256, takes a backup and waits for it, then switches to the new build. Apps keep serving; the API "+
			"restarts, and if the new build is not healthy within 90 seconds the previous one comes back. Returns once the switch "+
			"started; update status shows how it ended. Never installs an older or the same version. Box admins only.", tag)
	apply.Errors = append(apply.Errors, 409, 422)
	huma.Register(a, apply, api.Wrap(func(ctx context.Context, _ *struct{}) (*out, error) {
		u, err := admin(ctx)
		if err != nil {
			return nil, err
		}
		// The backup and the switch finish even when the caller stops waiting.
		up, err := u.apply(context.WithoutCancel(ctx), "now")
		var n nothing
		switch {
		case errors.Is(err, errBusy):
			return nil, api.NewProblem(409, "conflict", err.Error())
		case errors.As(err, &n):
			s := status(load(), time.Now())
			s.Summary = n.why
			return &out{s}, nil
		case err != nil:
			pb := api.NewProblem(502, "internal", "could not check for a release: "+err.Error())
			pb.Hint = "the box reads " + release.ManifestURL(load().source(), load().channel()) + "; check its network and try again"
			return nil, pb
		}
		pr := api.PrincipalFrom(ctx)
		_ = p.DB.Audit(ctx, pr.TokenID, "box.update_apply", up.ID, map[string]any{"to": up.To, "session": pr.Session})
		if up.Status == "failed" {
			pb := api.NewProblem(500, "internal", up.Summary)
			pb.Hint = "nothing was switched; update status has the record"
			return nil, pb
		}
		s := status(load(), time.Now())
		s.Summary = up.Summary + "; Tiffin restarts in a moment (update status shows how it ended)"
		return &out{s}, nil
	}))

	huma.Register(a, api.Op("update-settings", http.MethodPut, "/v1/box/update/settings", "update settings", api.RiskWrite,
		"Set how Tiffin updates",
		"Changes the settings it names and keeps the rest: auto (new releases install by themselves in the maintenance window), "+
			"channel (stable or edge), source (where the signed manifest is read) and window (HH:MM, server time). Box admins only.", tag),
		api.Wrap(func(ctx context.Context, in *struct{ Body settingsBody }) (*out, error) {
			if _, err := admin(ctx); err != nil {
				return nil, err
			}
			b := in.Body
			if b.Window != "" && b.Window != "box" {
				if err := platform.ValidRebootWindow(b.Window); err != nil {
					return nil, api.NewProblem(422, "validation", err.Error())
				}
			}
			r, err := edit(func(r *record) {
				if b.Auto != nil {
					r.Manual = !*b.Auto
				}
				if b.Channel != "" {
					r.Channel = b.Channel
				}
				switch b.Source {
				case "":
				case "default":
					r.Source = ""
				default:
					r.Source = b.Source
				}
				switch b.Window {
				case "":
				case "box":
					r.Window = ""
				default:
					r.Window = b.Window
				}
				if b.Channel != "" || b.Source != "" {
					r.Available, r.CheckError, r.NextCheck = nil, "", time.Time{} // check again
				}
			})
			if err != nil {
				return nil, err
			}
			pr := api.PrincipalFrom(ctx)
			_ = p.DB.Audit(ctx, pr.TokenID, "box.update_settings", "", map[string]any{"auto": !r.Manual, "channel": r.channel(),
				"source": r.source(), "window": window(r), "session": pr.Session})
			return &out{status(r, time.Now())}, nil
		}))
}
