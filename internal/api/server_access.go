package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/sshkeys"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// Server access (Settings › Server access) is the owner's SSH keys for the
// server: the public keys that log in as root, kept in the file sshd reads
// beside root's own (sshkeys.File). A key there is root on the machine, so
// only the owner sees or changes them: their dashboard session or the owner
// token, never an admin or an API key. A change in a dashboard session needs
// a recent strong sign-in; every change is in the audit log and emailed to
// the owner.

// ServerKeysFile is the managed key file sshd reads (tests point it at a
// temp dir). A box on a server serves it as Deps.ServerKeys.
var ServerKeysFile = sshkeys.File

// ServerKey is one SSH key that logs in to the server as root.
type ServerKey struct {
	Type        string    `json:"type" doc:"The key type: ssh-ed25519, sk-ssh-ed25519@openssh.com, ecdsa-sha2-nistp256, ssh-rsa..."`
	Fingerprint string    `json:"fingerprint" doc:"The key's SHA256 fingerprint, as ssh-keygen -lf shows it"`
	Bits        int       `json:"bits,omitempty" doc:"RSA keys only: the key's size in bits"`
	Source      string    `json:"source" doc:"Where it came from: pasted, hetzner (from the Hetzner project at setup), dashboard, or github:<user>"`
	Added       time.Time `json:"added" doc:"The day it was added (midnight UTC)"`
}

// ServerAccess is who can log in to the server over SSH, from Tiffin's list.
type ServerAccess struct {
	Keys []ServerKey `json:"keys" doc:"The keys Tiffin manages, oldest first. Keys added by hand to /root/.ssh/authorized_keys work too, but aren't listed."`
	Host string      `json:"host" doc:"The server's address: ssh root@<host>"`
	File string      `json:"file" doc:"The file on the server the keys are in"`
}

func serverKeys(keys []sshkeys.Key) []ServerKey {
	out := make([]ServerKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, ServerKey{Type: k.Type, Fingerprint: k.Fingerprint, Bits: k.Bits, Source: string(k.Source),
			Added: k.Added.UTC().Truncate(24 * time.Hour)})
	}
	return out
}

// serverHost is the box's own domain: the dashboard is dashboard.<host>, and
// ssh root@<host> reaches the server.
func (a *API) serverHost() string {
	if a.deps.Platform != nil && a.deps.Platform.Domain != "" {
		return a.deps.Platform.Domain
	}
	if u, err := url.Parse(a.deps.PublicURL); err == nil {
		return strings.TrimPrefix(u.Hostname(), "dashboard.")
	}
	return ""
}

func (a *API) serverAccess(keys []sshkeys.Key) *ServerAccess {
	return &ServerAccess{Keys: serverKeys(keys), Host: a.serverHost(), File: a.deps.ServerKeys.Path}
}

// serverAccessCaller lets in only the owner: their dashboard session, or the
// owner token (`tiffin server-access` on the server). An admin's session and
// every API key are refused, even an admin key: RequireSudo never asks keys.
func serverAccessCaller(p *tokens.Principal) error {
	switch {
	case p == nil:
		return problem(http.StatusUnauthorized, "unauthenticated", "sign in first")
	case p.Kind == tokens.KindOwner, p.IsSession() && p.Role == tokens.RoleOwner:
		return nil
	case p.IsSession():
		out := problem(http.StatusForbidden, "forbidden", "only the box's owner manages SSH access to the server")
		out.Hint = "ask the owner"
		return out
	}
	out := problem(http.StatusForbidden, "forbidden", "an API key can't see or change SSH access to the server: a key there logs in as root")
	out.Hint = "use the dashboard (Settings › Server access), or tiffin server-access on the server"
	return out
}

// serverKeysFor checks the caller (and, to change keys, a recent strong
// sign-in) and returns the key file.
func (a *API) serverKeysFor(ctx context.Context, write bool) (*tokens.Principal, *sshkeys.Store, error) {
	p := PrincipalFrom(ctx)
	if err := serverAccessCaller(p); err != nil {
		return nil, nil, err
	}
	if a.deps.ServerKeys == nil {
		return nil, nil, NewProblem(http.StatusNotImplemented, "internal", "server access is only on a box on a server; this one keeps the SSH setup it came with")
	}
	if write {
		if err := a.deps.Tokens.RequireSudo(ctx, p, tokens.ErrReauthSSH); err != nil {
			return nil, nil, err
		}
	}
	return p, a.deps.ServerKeys, nil
}

func (a *API) registerServerAccess() {
	api := a.api
	huma.Register(api, op("server-access", http.MethodGet, "/v1/server-access", "server-access list", RiskRead, "List SSH keys for the server",
		"The SSH keys that log in to the server as root (ssh root@<host>), from Settings › Server access or chosen at setup. "+
			"The owner only: their dashboard session, or the owner token. Admins and API keys are refused.", "server-access"),
		wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body *ServerAccess }, error) {
			_, st, err := a.serverKeysFor(ctx, false)
			if err != nil {
				return nil, err
			}
			keys, err := st.Read()
			if err != nil {
				return nil, err
			}
			return &struct{ Body *ServerAccess }{a.serverAccess(keys)}, nil
		}))

	add := op("server-key-add", http.MethodPost, "/v1/server-access/keys", "server-access add", RiskWrite, "Add SSH keys to the server",
		"Adds public keys (one per line, as in a .pub file or github.com/<user>.keys) that log in to the server as root. Accepted: ed25519, "+
			"security keys (sk-), ECDSA, and RSA of 3072 bits or more; no options (command=, from=...) and no certificates. Keys already there "+
			"are left as they are, so adding again is safe; at most 10 keys in all. The owner only: in a dashboard session it needs a sign-in "+
			"with a passkey, Google, GitHub or an emailed link in the last 10 minutes (reauth_required otherwise); the owner token is not asked. "+
			"API keys are refused. The owner is emailed about every key added.", "server-access")
	add.Middlewares = huma.Middlewares{withClientIP}
	huma.Register(api, add, wrap(func(ctx context.Context, in *struct {
		Body struct {
			Keys   string `json:"keys" minLength:"1" maxLength:"16384" doc:"One or more SSH public keys, one per line"`
			Source string `json:"source,omitempty" default:"pasted" pattern:"^(pasted|github:[A-Za-z0-9-]{1,39})$" doc:"Where the keys come from: pasted (the default), or github:<user> for keys from github.com/<user>.keys"`
		}
	}) (*struct{ Body *ServerAccess }, error) {
		p, st, err := a.serverKeysFor(ctx, true)
		if err != nil {
			return nil, err
		}
		src := sshkeys.Source(in.Body.Source)
		if src == "" {
			src = sshkeys.SourcePasted
		}
		if !src.Valid() {
			return nil, problem(422, "validation", "source: pasted, or github:<user> with a GitHub username")
		}
		keys, err := sshkeys.Parse(in.Body.Keys, src, time.Now())
		if err != nil {
			return nil, problem(422, "validation", err.Error())
		}
		var added []sshkeys.Key
		all, err := st.Update(func(have []sshkeys.Key) ([]sshkeys.Key, error) {
			there := map[string]bool{}
			for _, k := range have {
				there[k.Fingerprint] = true
			}
			added = added[:0]
			for _, k := range keys {
				if !there[k.Fingerprint] {
					added = append(added, k)
				}
			}
			next, err := sshkeys.Merge(have, keys)
			if err != nil {
				return nil, problem(422, "validation", err.Error())
			}
			return next, nil
		})
		if err != nil {
			return nil, err
		}
		for _, k := range added {
			_ = a.deps.DB.Audit(ctx, p.TokenID, "server_key.add", k.Fingerprint, map[string]any{
				"summary":     holder(p) + " added the SSH key " + k.Fingerprint + " (" + string(k.Source) + ") to the server",
				"fingerprint": k.Fingerprint, "type": k.Type, "source": string(k.Source)})
		}
		if len(added) > 0 {
			a.serverKeyNotice(ctx, p, BoxMailNewSSHKey, added)
		}
		return &struct{ Body *ServerAccess }{a.serverAccess(all)}, nil
	}))

	rm := op("server-key-remove", http.MethodPost, "/v1/server-access/keys/remove", "server-access remove", RiskDestructive, "Remove an SSH key from the server",
		"Removes one key, by its SHA256 fingerprint: it no longer logs in to the server. SSH sessions already open with it stay open until they end. "+
			"With no key left, your provider's console (Hetzner's, say) is the way in when you can't sign in to the dashboard. Same rules as adding: "+
			"the owner only, a recent strong sign-in in a dashboard session, never API keys. The owner is emailed about it.", "server-access")
	rm.Middlewares = huma.Middlewares{withClientIP}
	huma.Register(api, rm, wrap(func(ctx context.Context, in *struct {
		Body struct {
			Fingerprint string `json:"fingerprint" pattern:"^SHA256:[A-Za-z0-9+/]{43}$" doc:"The key's SHA256 fingerprint, as listed"`
		}
	}) (*struct{ Body *ServerAccess }, error) {
		p, st, err := a.serverKeysFor(ctx, true)
		if err != nil {
			return nil, err
		}
		var gone sshkeys.Key
		all, err := st.Update(func(have []sshkeys.Key) ([]sshkeys.Key, error) {
			for i, k := range have {
				if k.Fingerprint == in.Body.Fingerprint {
					gone = k
					return append(have[:i:i], have[i+1:]...), nil
				}
			}
			return nil, problem(http.StatusNotFound, "not_found", "no SSH key with that fingerprint on the server")
		})
		if err != nil {
			return nil, err
		}
		_ = a.deps.DB.Audit(ctx, p.TokenID, "server_key.delete", gone.Fingerprint, map[string]any{
			"summary":     holder(p) + " removed the SSH key " + gone.Fingerprint + " (" + string(gone.Source) + ") from the server",
			"fingerprint": gone.Fingerprint, "type": gone.Type, "source": string(gone.Source)})
		a.serverKeyNotice(ctx, p, BoxMailSSHKeyRemoved, []sshkeys.Key{gone})
		return &struct{ Body *ServerAccess }{a.serverAccess(all)}, nil
	}))
}

// holder names whoever changed the keys, for the audit log.
func holder(p *tokens.Principal) string {
	if p.PersonName != "" {
		return p.PersonName
	}
	return p.Name
}

// serverKeyNotice tells the owner that SSH keys on the server changed (kind
// is BoxMailNewSSHKey or BoxMailSSHKeyRemoved), with the browser and address
// it came from: a key added from a stolen session is root on the machine.
func (a *API) serverKeyNotice(ctx context.Context, by *tokens.Principal, kind string, keys []sshkeys.Key) {
	owner, err := a.deps.Tokens.GetPerson(ctx, tokens.OwnerPerson)
	if err != nil || owner.Email == "" {
		return
	}
	ip := clientIPFrom(ctx)
	device := ""
	if by.IsSession() {
		if c, err := a.deps.Tokens.SessionClient(ctx, by.TokenID); err == nil {
			device = c.Device
		}
	} else {
		device = "The tiffin CLI (owner token)"
	}
	a.sendLater(BoxMail{Kind: kind, To: owner.Email, Name: owner.Name, Role: owner.Role,
		Device: device, IP: ip, Where: locate(ip), At: time.Now().UTC(), SSHKeys: serverKeys(keys), Server: a.serverHost()})
}
