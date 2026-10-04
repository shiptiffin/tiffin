"use client";
import { jsx as _jsx, Fragment as _Fragment, jsxs as _jsxs } from "react/jsx-runtime";
import { useState } from "react";
import { errorText, useTiffinAuth } from "./client.js";
import { IconCheck, IconChevrons, IconOut, IconPasskey, IconPlus } from "./icons.js";
import { Avatar, Root, RoleChip, useMenu } from "./ui.js";
function isPersonal(o) {
    if (!o)
        return false;
    let m = o.metadata;
    if (typeof m === "string") {
        try {
            m = JSON.parse(m);
        }
        catch {
            m = null;
        }
    }
    return !!(m && typeof m === "object" && m.personal);
}
/** The signed-in person's avatar with a menu: who they are, add a passkey, sign out. */
export function UserButton(props) {
    const { client, config } = useTiffinAuth();
    const { data, isPending } = client.useSession();
    const m = useMenu();
    const [note, setNote] = useState(null);
    if (isPending)
        return _jsx(Root, { as: "span", className: props.className, theme: props.theme, children: _jsx(Avatar, { name: "", size: 32 }) });
    if (!data)
        return props.signedOut ? _jsx(_Fragment, { children: props.signedOut }) : null;
    const u = data.user;
    const addPasskey = async () => {
        setNote(null);
        const r = await client.passkey.addPasskey({ name: `${navigator.platform || "This device"}` });
        setNote(r?.error ? errorText(r.error, "That passkey wasn't saved.") : "Passkey saved. Next time, sign in with it.");
    };
    return (_jsx(Root, { as: "span", className: props.className, theme: props.theme, children: _jsxs("div", { className: "tf-pop-anchor", ref: m.anchor, children: [_jsx("button", { type: "button", className: "tf-trigger", "aria-label": `Account: ${u.name || u.email}`, ...m.triggerProps, children: _jsx(Avatar, { name: u.name, email: u.email, image: u.image, size: 32 }) }), m.open ? (_jsxs("div", { className: "tf-menu", role: "menu", "aria-label": "Account", ref: m.menu, onKeyDown: m.onKeyDown, children: [_jsxs("div", { className: "tf-menu-head", children: [_jsx(Avatar, { name: u.name, email: u.email, image: u.image, size: 38 }), _jsxs("div", { className: "tf-who", children: [_jsx("b", { children: u.name || u.email }), u.name ? _jsx("span", { children: u.email }) : null] })] }), _jsx("div", { className: "tf-menu-sep" }), config?.methods.includes("passkey") ? (_jsxs("button", { type: "button", role: "menuitem", className: "tf-item", onClick: () => void addPasskey(), children: [_jsx(IconPasskey, {}), _jsx("span", { className: "tf-grow", children: "Add a passkey" })] })) : null, props.children, _jsxs("button", { type: "button", role: "menuitem", className: "tf-item", onClick: async () => {
                                await client.signOut();
                                m.close(false);
                                if (typeof window !== "undefined")
                                    window.location.assign(props.afterSignOut ?? "/");
                            }, children: [_jsx(IconOut, {}), _jsx("span", { className: "tf-grow", children: "Sign out" })] }), note ? (_jsx("p", { className: "tf-hint", role: "status", style: { padding: "6px 10px 4px", margin: 0 }, children: note })) : null] })) : null] }) }));
}
/** Shows the active organization and switches between the person's organizations. */
export function OrgSwitcher(props) {
    const { client } = useTiffinAuth();
    const session = client.useSession();
    const active = client.useActiveOrganization();
    const list = client.useListOrganizations();
    const member = client.useActiveMember();
    const m = useMenu();
    const [creating, setCreating] = useState(false);
    const [name, setName] = useState("");
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState(null);
    if (!session.data)
        return null;
    const orgs = (list.data ?? []);
    const cur = active.data;
    const role = member.data?.role;
    const label = (o) => (isPersonal(o) ? session.data.user.name || "Personal" : o.name);
    const after = (id) => (props.onSwitch ? props.onSwitch(id) : typeof window !== "undefined" && window.location.reload());
    const choose = async (id) => {
        if (id === cur?.id)
            return m.close();
        const r = await client.organization.setActive({ organizationId: id });
        m.close();
        if (!r.error)
            after(id);
    };
    const create = async (e) => {
        e.preventDefault();
        if (!name.trim())
            return;
        setBusy(true);
        setError(null);
        const slug = name
            .trim()
            .toLowerCase()
            .replace(/[^a-z0-9]+/g, "-")
            .replace(/^-|-$/g, "")
            .slice(0, 40) +
            "-" +
            Math.random().toString(36).slice(2, 6);
        const r = await client.organization.create({ name: name.trim(), slug });
        setBusy(false);
        if (r.error)
            return setError(errorText(r.error));
        await client.organization.setActive({ organizationId: r.data.id });
        setCreating(false);
        setName("");
        m.close();
        after(r.data.id);
    };
    return (_jsx(Root, { as: "span", className: props.className, theme: props.theme, children: _jsxs("div", { className: "tf-pop-anchor", ref: m.anchor, children: [_jsxs("button", { type: "button", className: "tf-trigger", "data-variant": "org", "aria-label": `Organization: ${cur ? label(cur) : "none"}. Switch`, ...m.triggerProps, children: [_jsx(Avatar, { name: cur ? label(cur) : "?", image: cur?.logo, size: 28, square: true }), _jsxs("span", { className: "tf-trigger-text", children: [_jsx("b", { children: cur ? label(cur) : "Pick an organization" }), _jsx("span", { children: cur ? (isPersonal(cur) ? "Personal" : role ? role[0].toUpperCase() + role.slice(1) : "") : "" })] }), _jsx(IconChevrons, { className: "tf-chev" })] }), m.open ? (_jsxs("div", { className: "tf-menu", "data-align": "start", role: "menu", "aria-label": "Organizations", ref: m.menu, onKeyDown: m.onKeyDown, children: [_jsx("div", { className: "tf-menu-label", children: "Organizations" }), orgs.map((o) => (_jsxs("button", { type: "button", role: "menuitemradio", "aria-checked": o.id === cur?.id, className: "tf-item", onClick: () => void choose(o.id), children: [_jsx(Avatar, { name: label(o), image: o.logo, size: 24, square: true }), _jsx("span", { className: "tf-grow", children: label(o) }), isPersonal(o) ? _jsx("span", { className: "tf-meta", children: "Personal" }) : null, o.id === cur?.id ? _jsx(IconCheck, { className: "tf-check" }) : _jsx("span", { style: { width: 16 } })] }, o.id))), !props.hideCreate ? (_jsxs(_Fragment, { children: [_jsx("div", { className: "tf-menu-sep" }), creating ? (_jsxs("form", { className: "tf-inline-form", onSubmit: create, children: [_jsx("input", { className: "tf-input", "aria-label": "Organization name", placeholder: "Acme Inc.", autoFocus: true, value: name, onChange: (e) => setName(e.target.value) }), _jsx("button", { type: "submit", className: "tf-btn tf-btn-primary tf-btn-sm", disabled: busy || !name.trim(), "aria-busy": busy || undefined, children: _jsx("span", { children: "Create" }) })] })) : (_jsxs("button", { type: "button", role: "menuitem", className: "tf-item", onClick: () => setCreating(true), children: [_jsx(IconPlus, {}), _jsx("span", { className: "tf-grow", children: "Create organization" })] })), error ? (_jsx("p", { className: "tf-hint", role: "alert", style: { color: "var(--tf-danger)", padding: "4px 10px", margin: 0 }, children: error })) : null] })) : null] })) : null] }) }));
}
export { RoleChip };
