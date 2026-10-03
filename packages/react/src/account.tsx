"use client";
import { useState, type FormEvent, type ReactNode } from "react";
import { errorText, useTiffinAuth } from "./client";
import { IconCheck, IconChevrons, IconOut, IconPasskey, IconPlus } from "./icons";
import { Avatar, Root, RoleChip, useMenu, type Common } from "./ui";

type Org = { id: string; name: string; slug: string; logo?: string | null; metadata?: unknown };

function isPersonal(o: Org | null | undefined): boolean {
  if (!o) return false;
  let m = o.metadata;
  if (typeof m === "string") {
    try {
      m = JSON.parse(m);
    } catch {
      m = null;
    }
  }
  return !!(m && typeof m === "object" && (m as { personal?: boolean }).personal);
}

export type UserButtonProps = Common & {
  /** Where to go after signing out. Default "/". */
  afterSignOut?: string;
  /** Extra menu items, rendered above "Sign out". */
  children?: ReactNode;
  /** Shown while signed out (e.g. a sign-in link). Default: nothing. */
  signedOut?: ReactNode;
};

/** The signed-in person's avatar with a menu: who they are, add a passkey, sign out. */
export function UserButton(props: UserButtonProps) {
  const { client, config } = useTiffinAuth();
  const { data, isPending } = client.useSession();
  const m = useMenu();
  const [note, setNote] = useState<string | null>(null);
  if (isPending) return <Root as="span" className={props.className} theme={props.theme}><Avatar name="" size={32} /></Root>;
  if (!data) return props.signedOut ? <>{props.signedOut}</> : null;
  const u = data.user;
  const addPasskey = async () => {
    setNote(null);
    const r = await client.passkey.addPasskey({ name: `${navigator.platform || "This device"}` });
    setNote(r?.error ? errorText(r.error, "That passkey wasn't saved.") : "Passkey saved. Next time, sign in with it.");
  };
  return (
    <Root as="span" className={props.className} theme={props.theme}>
      <div className="tf-pop-anchor" ref={m.anchor}>
        <button type="button" className="tf-trigger" aria-label={`Account: ${u.name || u.email}`} {...m.triggerProps}>
          <Avatar name={u.name} email={u.email} image={u.image} size={32} />
        </button>
        {m.open ? (
          <div className="tf-menu" role="menu" aria-label="Account" ref={m.menu} onKeyDown={m.onKeyDown}>
            <div className="tf-menu-head">
              <Avatar name={u.name} email={u.email} image={u.image} size={38} />
              <div className="tf-who">
                <b>{u.name || u.email}</b>
                {u.name ? <span>{u.email}</span> : null}
              </div>
            </div>
            <div className="tf-menu-sep" />
            {config?.methods.includes("passkey") ? (
              <button type="button" role="menuitem" className="tf-item" onClick={() => void addPasskey()}>
                <IconPasskey />
                <span className="tf-grow">Add a passkey</span>
              </button>
            ) : null}
            {props.children}
            <button
              type="button"
              role="menuitem"
              className="tf-item"
              onClick={async () => {
                await client.signOut();
                m.close(false);
                if (typeof window !== "undefined") window.location.assign(props.afterSignOut ?? "/");
              }}
            >
              <IconOut />
              <span className="tf-grow">Sign out</span>
            </button>
            {note ? (
              <p className="tf-hint" role="status" style={{ padding: "6px 10px 4px", margin: 0 }}>
                {note}
              </p>
            ) : null}
          </div>
        ) : null}
      </div>
    </Root>
  );
}

export type OrgSwitcherProps = Common & {
  /** Called after switching (default: reload the page so server data follows). */
  onSwitch?: (organizationId: string) => void;
  /** Hide "Create organization". */
  hideCreate?: boolean;
};

/** Shows the active organization and switches between the person's organizations. */
export function OrgSwitcher(props: OrgSwitcherProps) {
  const { client } = useTiffinAuth();
  const session = client.useSession();
  const active = client.useActiveOrganization();
  const list = client.useListOrganizations();
  const member = client.useActiveMember();
  const m = useMenu();
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  if (!session.data) return null;
  const orgs = (list.data ?? []) as Org[];
  const cur = active.data as Org | null;
  const role = (member.data as { role?: string } | null)?.role;
  const label = (o: Org) => (isPersonal(o) ? session.data!.user.name || "Personal" : o.name);
  const after = (id: string) => (props.onSwitch ? props.onSwitch(id) : typeof window !== "undefined" && window.location.reload());

  const choose = async (id: string) => {
    if (id === cur?.id) return m.close();
    const r = await client.organization.setActive({ organizationId: id });
    m.close();
    if (!r.error) after(id);
  };
  const create = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    setError(null);
    const slug =
      name
        .trim()
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-|-$/g, "")
        .slice(0, 40) +
      "-" +
      Math.random().toString(36).slice(2, 6);
    const r = await client.organization.create({ name: name.trim(), slug });
    setBusy(false);
    if (r.error) return setError(errorText(r.error));
    await client.organization.setActive({ organizationId: r.data!.id });
    setCreating(false);
    setName("");
    m.close();
    after(r.data!.id);
  };

  return (
    <Root as="span" className={props.className} theme={props.theme}>
      <div className="tf-pop-anchor" ref={m.anchor}>
        <button type="button" className="tf-trigger" data-variant="org" aria-label={`Organization: ${cur ? label(cur) : "none"}. Switch`} {...m.triggerProps}>
          <Avatar name={cur ? label(cur) : "?"} image={cur?.logo} size={28} square />
          <span className="tf-trigger-text">
            <b>{cur ? label(cur) : "Pick an organization"}</b>
            <span>{cur ? (isPersonal(cur) ? "Personal" : role ? role[0]!.toUpperCase() + role.slice(1) : "") : ""}</span>
          </span>
          <IconChevrons className="tf-chev" />
        </button>
        {m.open ? (
          <div className="tf-menu" data-align="start" role="menu" aria-label="Organizations" ref={m.menu} onKeyDown={m.onKeyDown}>
            <div className="tf-menu-label">Organizations</div>
            {orgs.map((o) => (
              <button key={o.id} type="button" role="menuitemradio" aria-checked={o.id === cur?.id} className="tf-item" onClick={() => void choose(o.id)}>
                <Avatar name={label(o)} image={o.logo} size={24} square />
                <span className="tf-grow">{label(o)}</span>
                {isPersonal(o) ? <span className="tf-meta">Personal</span> : null}
                {o.id === cur?.id ? <IconCheck className="tf-check" /> : <span style={{ width: 16 }} />}
              </button>
            ))}
            {!props.hideCreate ? (
              <>
                <div className="tf-menu-sep" />
                {creating ? (
                  <form className="tf-inline-form" onSubmit={create}>
                    <input className="tf-input" aria-label="Organization name" placeholder="Acme Inc." autoFocus value={name} onChange={(e) => setName(e.target.value)} />
                    <button type="submit" className="tf-btn tf-btn-primary tf-btn-sm" disabled={busy || !name.trim()} aria-busy={busy || undefined}>
                      <span>Create</span>
                    </button>
                  </form>
                ) : (
                  <button type="button" role="menuitem" className="tf-item" onClick={() => setCreating(true)}>
                    <IconPlus />
                    <span className="tf-grow">Create organization</span>
                  </button>
                )}
                {error ? (
                  <p className="tf-hint" role="alert" style={{ color: "var(--tf-danger)", padding: "4px 10px", margin: 0 }}>
                    {error}
                  </p>
                ) : null}
              </>
            ) : null}
          </div>
        ) : null}
      </div>
    </Root>
  );
}

export { RoleChip };
