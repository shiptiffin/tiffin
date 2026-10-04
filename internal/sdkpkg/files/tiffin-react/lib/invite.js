"use client";
import { jsx as _jsx, Fragment as _Fragment, jsxs as _jsxs } from "react/jsx-runtime";
import { useCallback, useEffect, useState } from "react";
import { errorText, useTiffinAuth } from "./client.js";
import { IconCopy, IconLink, IconUsers } from "./icons.js";
import { SignIn } from "./sign-in.js";
import { Alert, Avatar, Button, Card, Field, Head, Root, RoleChip } from "./ui.js";
const ROLES = ["viewer", "member", "admin", "owner"];
const rank = (r) => Math.max(-1, ...(r ?? "").split(",").map((x) => ROLES.indexOf(x.trim())));
const ROLE_HELP = {
    viewer: "Can see everything, change nothing.",
    member: "Can work on things in the organization.",
    admin: "Can also invite people and manage members.",
    owner: "Full control, including deleting the organization.",
};
/**
 * Invite people to an organization by email or with a shareable link, and
 * see pending invitations. Roles above the inviter's own are not offered.
 */
export function Invite(props) {
    const { client, baseURL } = useTiffinAuth();
    const active = client.useActiveOrganization();
    const member = client.useActiveMember();
    const orgId = props.organizationId ?? active.data?.id;
    const orgName = active.data?.name;
    const myRole = member.data?.role ?? null;
    const canInvite = rank(myRole) >= rank("admin");
    const offered = ROLES.filter((r) => rank(r) <= rank(myRole));
    const [email, setEmail] = useState("");
    const [role, setRole] = useState("member");
    const [busy, setBusy] = useState(null);
    const [error, setError] = useState(null);
    const [ok, setOk] = useState(null);
    const [invites, setInvites] = useState([]);
    const [links, setLinks] = useState([]);
    const [fresh, setFresh] = useState(null);
    const [copied, setCopied] = useState(false);
    const refresh = useCallback(async () => {
        if (!orgId || !canInvite)
            return;
        const r = await client.organization.listInvitations({ query: { organizationId: orgId } });
        if (r.data)
            setInvites(r.data.filter((i) => i.status === "pending"));
        if (!props.hideLinks) {
            const l = await fetch(`${baseURL}/api/auth/invite-link/list?organizationId=${encodeURIComponent(orgId)}`, { credentials: "include" });
            if (l.ok)
                setLinks((await l.json()).filter((x) => x.active));
        }
    }, [orgId, canInvite, baseURL]);
    useEffect(() => {
        void refresh();
    }, [refresh]);
    if (!orgId || member.isPending)
        return null;
    if (!canInvite) {
        return (_jsx(Root, { className: props.className, theme: props.theme, children: _jsx(Card, { label: "Invite people", children: _jsx(Head, { title: "Invite people", sub: "Only owners and admins can invite people. Ask one of them to add who you need." }) }) }));
    }
    const send = async (e) => {
        e.preventDefault();
        setBusy("email");
        setError(null);
        setOk(null);
        const r = await client.organization.inviteMember({ email: email.trim(), role: role, organizationId: orgId });
        setBusy(null);
        if (r.error)
            return setError(errorText(r.error));
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
        const body = (await r.json().catch(() => ({})));
        if (!r.ok)
            return setError(body.message ?? "Couldn't create the link.");
        setFresh({ url: body.url, role: body.role });
        setCopied(false);
        void refresh();
    };
    const copy = async () => {
        if (!fresh)
            return;
        await navigator.clipboard?.writeText(fresh.url).catch(() => { });
        setCopied(true);
    };
    const cancel = async (id) => {
        await client.organization.cancelInvitation({ invitationId: id });
        void refresh();
    };
    const revoke = async (id) => {
        await fetch(`${baseURL}/api/auth/invite-link/revoke`, { method: "POST", credentials: "include", headers: { "content-type": "application/json" }, body: JSON.stringify({ id }) });
        if (fresh)
            setFresh(null);
        void refresh();
    };
    return (_jsx(Root, { className: props.className, theme: props.theme, children: _jsxs(Card, { label: "Invite people", children: [_jsx(Head, { title: "Invite people", sub: orgName ? _jsxs(_Fragment, { children: ["Bring your team into ", _jsx("b", { children: orgName }), "."] }) : "Bring your team in." }), _jsxs("form", { className: "tf-stack", onSubmit: send, children: [_jsx(Field, { label: "Email", type: "email", required: true, autoComplete: "off", placeholder: "teammate@example.com", value: email, onChange: (e) => setEmail(e.target.value) }), _jsxs("div", { className: "tf-field", children: [_jsx("span", { className: "tf-label", id: "tf-role-label", children: "Role" }), _jsx("div", { className: "tf-seg", role: "radiogroup", "aria-labelledby": "tf-role-label", children: ROLES.map((r) => (_jsx("button", { type: "button", role: "radio", "aria-checked": role === r, disabled: !offered.includes(r), title: !offered.includes(r) ? "Above your own role" : undefined, onClick: () => setRole(r), children: r }, r))) }), _jsx("span", { className: "tf-hint", children: ROLE_HELP[role] })] }), error ? _jsx(Alert, { children: error }) : null, ok ? _jsx(Alert, { tone: "ok", children: ok }) : null, _jsx(Button, { type: "submit", busy: busy === "email", children: "Send invitation" })] }), !props.hideLinks ? (_jsxs("div", { className: "tf-section", children: [_jsx("h3", { className: "tf-section-title", children: "Or share a link" }), fresh ? (_jsxs("div", { className: "tf-stack", children: [_jsxs("div", { className: "tf-copy", children: [_jsx(IconLink, { width: 15, height: 15, style: { flex: "none" } }), _jsx("code", { children: fresh.url }), _jsx(Button, { small: true, variant: "quiet", type: "button", icon: _jsx(IconCopy, {}), onClick: () => void copy(), children: copied ? "Copied" : "Copy" })] }), _jsxs("span", { className: "tf-hint", children: ["Anyone with this link joins as ", _jsx("b", { children: fresh.role }), ". Works 25 times over 7 days; turn it off below."] })] })) : (_jsxs(Button, { variant: "quiet", type: "button", icon: _jsx(IconLink, {}), busy: busy === "link", onClick: () => void makeLink(), children: ["Create a ", role, " invite link"] }))] })) : null, invites.length || links.length ? (_jsxs("div", { className: "tf-section", children: [_jsx("h3", { className: "tf-section-title", children: "Pending" }), _jsxs("ul", { className: "tf-list", children: [invites.map((i) => (_jsxs("li", { children: [_jsx(Avatar, { email: i.email, size: 26 }), _jsx("span", { className: "tf-grow", children: i.email }), _jsx(RoleChip, { role: i.role }), _jsx("button", { type: "button", className: "tf-link", onClick: () => void cancel(i.id), "aria-label": `Cancel invitation to ${i.email}`, children: "Cancel" })] }, i.id))), links.map((l) => (_jsxs("li", { children: [_jsx("span", { className: "tf-avatar", style: { ["--tf-size"]: "26px" }, "aria-hidden": true, children: _jsx(IconLink, { width: 13, height: 13 }) }), _jsxs("span", { className: "tf-grow", children: ["Invite link ", _jsxs("span", { className: "tf-meta", children: ["\u00B7 ", l.uses, " of ", l.maxUses, " used"] })] }), _jsx(RoleChip, { role: l.role }), _jsx("button", { type: "button", className: "tf-link", onClick: () => void revoke(l.id), children: "Turn off" })] }, l.id)))] })] })) : null] }) }));
}
/** The page invitation emails and invite links point at: shows the org, signs in if needed, joins. */
export function AcceptInvite(props) {
    const { client, baseURL } = useTiffinAuth();
    const session = client.useSession();
    const qs = typeof window !== "undefined" ? new URLSearchParams(window.location.search) : new URLSearchParams();
    const invitationId = props.invitationId ?? qs.get("invitation") ?? undefined;
    const token = props.token ?? qs.get("link") ?? undefined;
    const [preview, setPreview] = useState(null);
    const [busy, setBusy] = useState(null);
    const [error, setError] = useState(null);
    const [declined, setDeclined] = useState(false);
    useEffect(() => {
        if (session.isPending)
            return;
        let live = true;
        (async () => {
            if (token) {
                const r = await fetch(`${baseURL}/api/auth/invite-link/get?token=${encodeURIComponent(token)}`, { credentials: "include" });
                const b = (await r.json().catch(() => null));
                if (live)
                    setPreview({ organization: b?.organization ?? null, role: b?.role ?? null, error: b?.valid ? null : (b?.reason ?? "This invite link isn't valid.") });
            }
            else if (invitationId && session.data) {
                const r = await client.organization.getInvitation({ query: { id: invitationId } });
                const d = r.data;
                if (!live)
                    return;
                if (r.error || !d)
                    setPreview({ organization: null, role: null, error: r.error?.status === 403 || r.error?.status === 400 ? `This invitation was sent to another email address than ${session.data.user.email}, or it has expired.` : errorText(r.error, "This invitation isn't valid anymore.") });
                else
                    setPreview({ organization: { id: d.organizationId, name: d.organizationName }, role: d.role, inviter: d.inviterEmail, email: d.email });
            }
            else if (!invitationId) {
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
            const b = (await r.json().catch(() => ({})));
            if (!r.ok) {
                setBusy(null);
                return setError(b.message ?? "Couldn't join with this link.");
            }
            orgId = b.member?.organizationId ?? orgId;
        }
        else if (invitationId) {
            const r = await client.organization.acceptInvitation({ invitationId });
            if (r.error) {
                setBusy(null);
                return setError(errorText(r.error));
            }
        }
        await client.organization.setActive({ organizationId: orgId });
        setBusy(null);
        if (props.onAccepted)
            props.onAccepted(orgId);
        else if (typeof window !== "undefined")
            window.location.assign(props.redirectTo ?? "/");
    };
    const decline = async () => {
        if (!invitationId)
            return setDeclined(true);
        setBusy("decline");
        await client.organization.rejectInvitation({ invitationId });
        setBusy(null);
        setDeclined(true);
    };
    if (session.isPending)
        return null;
    // Not signed in: an email invitation needs the invited address; a link works for anyone.
    if (!session.data) {
        return (_jsx(SignIn, { className: props.className, theme: props.theme, logo: props.logo, title: "Sign in to accept your invitation", redirectTo: typeof window !== "undefined" ? window.location.pathname + window.location.search : "/", onSignedIn: () => typeof window !== "undefined" && window.location.reload() }));
    }
    const org = preview?.organization;
    return (_jsx(Root, { className: props.className, theme: props.theme, children: _jsxs(Card, { label: "Invitation", children: [props.logo ? _jsx("div", { className: "tf-logo", children: props.logo }) : null, !preview ? (_jsx("div", { "aria-busy": "true", style: { height: 140 } })) : declined ? (_jsx("div", { className: "tf-view", children: _jsx(Head, { title: "Invitation declined", sub: "No worries. If you change your mind, ask for a new invitation." }) })) : preview.error ? (_jsxs("div", { className: "tf-view", children: [_jsx("div", { className: "tf-badge-icon", children: _jsx(IconUsers, {}) }), _jsx(Head, { title: "This invitation can't be used", sub: preview.error })] })) : (_jsxs("div", { className: "tf-view", children: [_jsxs("div", { className: "tf-org-mark", children: [_jsx(Avatar, { name: org?.name, image: org?.logo, size: 44, square: true }), _jsxs("div", { className: "tf-who", style: { minWidth: 0 }, children: [_jsx("b", { style: { fontSize: 15.5 }, children: org?.name }), _jsxs("span", { children: ["You'll join as ", _jsx(RoleChip, { role: preview.role ?? "member" })] })] })] }), _jsx(Head, { title: `Join ${org?.name ?? "the team"}`, sub: preview.inviter ? _jsxs(_Fragment, { children: [preview.inviter, " invited you. You're signed in as ", _jsx("b", { children: session.data.user.email }), "."] }) : _jsxs(_Fragment, { children: ["You're signed in as ", _jsx("b", { children: session.data.user.email }), "."] }) }), _jsxs("div", { className: "tf-stack", children: [error ? _jsx(Alert, { children: error }) : null, _jsx(Button, { busy: busy === "accept", onClick: () => void accept(), children: "Accept invitation" }), invitationId ? (_jsx(Button, { variant: "quiet", busy: busy === "decline", onClick: () => void decline(), children: "Decline" })) : null] })] }))] }) }));
}
