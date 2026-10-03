"use client";
import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import { errorText, useTiffinAuth } from "./client";
import { IconCopy, IconLink, IconUsers } from "./icons";
import { SignIn } from "./sign-in";
import { Alert, Avatar, Button, Card, Field, Head, Root, RoleChip, type Common } from "./ui";

const ROLES = ["viewer", "member", "admin", "owner"] as const;
type Role = (typeof ROLES)[number];
const rank = (r?: string | null) => Math.max(-1, ...(r ?? "").split(",").map((x) => ROLES.indexOf(x.trim() as Role)));
const ROLE_HELP: Record<Role, string> = {
  viewer: "Can see everything, change nothing.",
  member: "Can work on things in the organization.",
  admin: "Can also invite people and manage members.",
  owner: "Full control, including deleting the organization.",
};

type Invitation = { id: string; email: string; role: string; status: string; expiresAt: string };
type InviteLink = { id: string; role: string; maxUses: number; uses: number; expiresAt: string; active: boolean };

export type InviteProps = Common & {
  /** Organization to invite to. Default: the active one. */
  organizationId?: string;
  /** Hide the shareable-link section. */
  hideLinks?: boolean;
};

/**
 * Invite people to an organization by email or with a shareable link, and
 * see pending invitations. Roles above the inviter's own are not offered.
 */
export function Invite(props: InviteProps) {
  const { client, baseURL } = useTiffinAuth();
  const active = client.useActiveOrganization();
  const member = client.useActiveMember();
  const orgId = props.organizationId ?? (active.data as { id?: string } | null)?.id;
  const orgName = (active.data as { name?: string } | null)?.name;
  const myRole = (member.data as { role?: string } | null)?.role ?? null;
  const canInvite = rank(myRole) >= rank("admin");
  const offered = ROLES.filter((r) => rank(r) <= rank(myRole));

  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role>("member");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState<string | null>(null);
  const [invites, setInvites] = useState<Invitation[]>([]);
  const [links, setLinks] = useState<InviteLink[]>([]);
  const [fresh, setFresh] = useState<{ url: string; role: string } | null>(null);
  const [copied, setCopied] = useState(false);

  const refresh = useCallback(async () => {
    if (!orgId || !canInvite) return;
    const r = await client.organization.listInvitations({ query: { organizationId: orgId } });
    if (r.data) setInvites((r.data as unknown as Invitation[]).filter((i) => i.status === "pending"));
    if (!props.hideLinks) {
      const l = await fetch(`${baseURL}/api/auth/invite-link/list?organizationId=${encodeURIComponent(orgId)}`, { credentials: "include" });
      if (l.ok) setLinks(((await l.json()) as InviteLink[]).filter((x) => x.active));
    }
  }, [orgId, canInvite, baseURL]);
  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!orgId || member.isPending) return null;
  if (!canInvite) {
    return (
      <Root className={props.className} theme={props.theme}>
        <Card label="Invite people">
          <Head title="Invite people" sub="Only owners and admins can invite people. Ask one of them to add who you need." />
        </Card>
      </Root>
    );
  }

  const send = async (e: FormEvent) => {
    e.preventDefault();
    setBusy("email");
    setError(null);
    setOk(null);
    const r = await client.organization.inviteMember({ email: email.trim(), role: role as "member", organizationId: orgId });
    setBusy(null);
    if (r.error) return setError(errorText(r.error));
    setOk(`Invitation sent to ${email.trim()}. It's good for 48 hours.`);
    setEmail("");
    void refresh();
  };
  const makeLink = async () => {
    setBusy("link");
    setError(null);
    const r = await fetch(`${baseURL}/api/auth/invite-link/create`, {
      method: "POST",
      credentials: "include",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ organizationId: orgId, role, maxUses: 25, expiresInDays: 7 }),
    });
    setBusy(null);
    const body = (await r.json().catch(() => ({}))) as { url?: string; role?: string; message?: string };
    if (!r.ok) return setError(body.message ?? "Couldn't create the link.");
    setFresh({ url: body.url!, role: body.role! });
    setCopied(false);
    void refresh();
  };
  const copy = async () => {
    if (!fresh) return;
    await navigator.clipboard?.writeText(fresh.url).catch(() => {});
    setCopied(true);
  };
  const cancel = async (id: string) => {
    await client.organization.cancelInvitation({ invitationId: id });
    void refresh();
  };
  const revoke = async (id: string) => {
    await fetch(`${baseURL}/api/auth/invite-link/revoke`, { method: "POST", credentials: "include", headers: { "content-type": "application/json" }, body: JSON.stringify({ id }) });
    if (fresh) setFresh(null);
    void refresh();
  };

  return (
    <Root className={props.className} theme={props.theme}>
      <Card label="Invite people">
        <Head title="Invite people" sub={orgName ? <>Bring your team into <b>{orgName}</b>.</> : "Bring your team in."} />
        <form className="tf-stack" onSubmit={send}>
          <Field label="Email" type="email" required autoComplete="off" placeholder="teammate@example.com" value={email} onChange={(e) => setEmail(e.target.value)} />
          <div className="tf-field">
            <span className="tf-label" id="tf-role-label">
              Role
            </span>
            <div className="tf-seg" role="radiogroup" aria-labelledby="tf-role-label">
              {ROLES.map((r) => (
                <button key={r} type="button" role="radio" aria-checked={role === r} disabled={!offered.includes(r)} title={!offered.includes(r) ? "Above your own role" : undefined} onClick={() => setRole(r)}>
                  {r}
                </button>
              ))}
            </div>
            <span className="tf-hint">{ROLE_HELP[role]}</span>
          </div>
          {error ? <Alert>{error}</Alert> : null}
          {ok ? <Alert tone="ok">{ok}</Alert> : null}
          <Button type="submit" busy={busy === "email"}>
            Send invitation
          </Button>
        </form>

        {!props.hideLinks ? (
          <div className="tf-section">
            <h3 className="tf-section-title">Or share a link</h3>
            {fresh ? (
              <div className="tf-stack">
                <div className="tf-copy">
                  <IconLink width={15} height={15} style={{ flex: "none" }} />
                  <code>{fresh.url}</code>
                  <Button small variant="quiet" type="button" icon={<IconCopy />} onClick={() => void copy()}>
                    {copied ? "Copied" : "Copy"}
                  </Button>
                </div>
                <span className="tf-hint">
                  Anyone with this link joins as <b>{fresh.role}</b>. Works 25 times over 7 days; turn it off below.
                </span>
              </div>
            ) : (
              <Button variant="quiet" type="button" icon={<IconLink />} busy={busy === "link"} onClick={() => void makeLink()}>
                Create a {role} invite link
              </Button>
            )}
          </div>
        ) : null}

        {invites.length || links.length ? (
          <div className="tf-section">
            <h3 className="tf-section-title">Pending</h3>
            <ul className="tf-list">
              {invites.map((i) => (
                <li key={i.id}>
                  <Avatar email={i.email} size={26} />
                  <span className="tf-grow">{i.email}</span>
                  <RoleChip role={i.role} />
                  <button type="button" className="tf-link" onClick={() => void cancel(i.id)} aria-label={`Cancel invitation to ${i.email}`}>
                    Cancel
                  </button>
                </li>
              ))}
              {links.map((l) => (
                <li key={l.id}>
                  <span className="tf-avatar" style={{ ["--tf-size" as string]: "26px" }} aria-hidden>
                    <IconLink width={13} height={13} />
                  </span>
                  <span className="tf-grow">
                    Invite link <span className="tf-meta">· {l.uses} of {l.maxUses} used</span>
                  </span>
                  <RoleChip role={l.role} />
                  <button type="button" className="tf-link" onClick={() => void revoke(l.id)}>
                    Turn off
                  </button>
                </li>
              ))}
            </ul>
          </div>
        ) : null}
      </Card>
    </Root>
  );
}

export type AcceptInviteProps = Common & {
  /** Email invitation id (default: ?invitation= in the URL). */
  invitationId?: string;
  /** Invite link token (default: ?link= in the URL). */
  token?: string;
  /** Where to go after joining. Default "/". */
  redirectTo?: string;
  onAccepted?: (organizationId: string) => void;
  logo?: ReactNode;
};

type Preview = { organization: { id: string; name: string; logo?: string | null } | null; role: string | null; inviter?: string | null; email?: string | null; error?: string | null };

/** The page invitation emails and invite links point at: shows the org, signs in if needed, joins. */
export function AcceptInvite(props: AcceptInviteProps) {
  const { client, baseURL } = useTiffinAuth();
  const session = client.useSession();
  const qs = typeof window !== "undefined" ? new URLSearchParams(window.location.search) : new URLSearchParams();
  const invitationId = props.invitationId ?? qs.get("invitation") ?? undefined;
  const token = props.token ?? qs.get("link") ?? undefined;
  const [preview, setPreview] = useState<Preview | null>(null);
  const [busy, setBusy] = useState<"accept" | "decline" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [declined, setDeclined] = useState(false);

  useEffect(() => {
    if (session.isPending) return;
    let live = true;
    (async () => {
      if (token) {
        const r = await fetch(`${baseURL}/api/auth/invite-link/get?token=${encodeURIComponent(token)}`, { credentials: "include" });
        const b = (await r.json().catch(() => null)) as { valid?: boolean; reason?: string; organization?: Preview["organization"]; role?: string } | null;
        if (live) setPreview({ organization: b?.organization ?? null, role: b?.role ?? null, error: b?.valid ? null : (b?.reason ?? "This invite link isn't valid.") });
      } else if (invitationId && session.data) {
        const r = await client.organization.getInvitation({ query: { id: invitationId } });
        const d = r.data as { organizationId: string; organizationName: string; role: string; inviterEmail?: string; email: string } | null;
        if (!live) return;
        if (r.error || !d) setPreview({ organization: null, role: null, error: r.error?.status === 403 || r.error?.status === 400 ? `This invitation was sent to another email address than ${session.data.user.email}, or it has expired.` : errorText(r.error, "This invitation isn't valid anymore.") });
        else setPreview({ organization: { id: d.organizationId, name: d.organizationName }, role: d.role, inviter: d.inviterEmail, email: d.email });
      } else if (!invitationId) {
        setPreview({ organization: null, role: null, error: "This page needs an invitation link. Open the link from your email again." });
      }
    })();
    return () => {
      live = false;
    };
  }, [token, invitationId, session.isPending, !!session.data]);

  const accept = async () => {
    setBusy("accept");
    setError(null);
    let orgId = preview?.organization?.id ?? "";
    if (token) {
      const r = await fetch(`${baseURL}/api/auth/invite-link/accept`, { method: "POST", credentials: "include", headers: { "content-type": "application/json" }, body: JSON.stringify({ token }) });
      const b = (await r.json().catch(() => ({}))) as { member?: { organizationId: string }; message?: string };
      if (!r.ok) {
        setBusy(null);
        return setError(b.message ?? "Couldn't join with this link.");
      }
      orgId = b.member?.organizationId ?? orgId;
    } else if (invitationId) {
      const r = await client.organization.acceptInvitation({ invitationId });
      if (r.error) {
        setBusy(null);
        return setError(errorText(r.error));
      }
    }
    await client.organization.setActive({ organizationId: orgId });
    setBusy(null);
    if (props.onAccepted) props.onAccepted(orgId);
    else if (typeof window !== "undefined") window.location.assign(props.redirectTo ?? "/");
  };
  const decline = async () => {
    if (!invitationId) return setDeclined(true);
    setBusy("decline");
    await client.organization.rejectInvitation({ invitationId });
    setBusy(null);
    setDeclined(true);
  };

  if (session.isPending) return null;
  // Not signed in: an email invitation needs the invited address; a link works for anyone.
  if (!session.data) {
    return (
      <SignIn
        className={props.className}
        theme={props.theme}
        logo={props.logo}
        title="Sign in to accept your invitation"
        redirectTo={typeof window !== "undefined" ? window.location.pathname + window.location.search : "/"}
        onSignedIn={() => typeof window !== "undefined" && window.location.reload()}
      />
    );
  }
  const org = preview?.organization;
  return (
    <Root className={props.className} theme={props.theme}>
      <Card label="Invitation">
        {props.logo ? <div className="tf-logo">{props.logo}</div> : null}
        {!preview ? (
          <div aria-busy="true" style={{ height: 140 }} />
        ) : declined ? (
          <div className="tf-view">
            <Head title="Invitation declined" sub="No worries. If you change your mind, ask for a new invitation." />
          </div>
        ) : preview.error ? (
          <div className="tf-view">
            <div className="tf-badge-icon">
              <IconUsers />
            </div>
            <Head title="This invitation can't be used" sub={preview.error} />
          </div>
        ) : (
          <div className="tf-view">
            <div className="tf-org-mark">
              <Avatar name={org?.name} image={org?.logo} size={44} square />
              <div className="tf-who" style={{ minWidth: 0 }}>
                <b style={{ fontSize: 15.5 }}>{org?.name}</b>
                <span>
                  You'll join as <RoleChip role={preview.role ?? "member"} />
                </span>
              </div>
            </div>
            <Head title={`Join ${org?.name ?? "the team"}`} sub={preview.inviter ? <>{preview.inviter} invited you. You're signed in as <b>{session.data.user.email}</b>.</> : <>You're signed in as <b>{session.data.user.email}</b>.</>} />
            <div className="tf-stack">
              {error ? <Alert>{error}</Alert> : null}
              <Button busy={busy === "accept"} onClick={() => void accept()}>
                Accept invitation
              </Button>
              {invitationId ? (
                <Button variant="quiet" busy={busy === "decline"} onClick={() => void decline()}>
                  Decline
                </Button>
              ) : null}
            </div>
          </div>
        )}
      </Card>
    </Root>
  );
}
