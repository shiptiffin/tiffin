export function dto(s) {
    const o = s.organization;
    return {
        user: { id: s.user.id, email: s.user.email, name: s.user.name, image: s.user.image ?? null, emailVerified: !!s.user.emailVerified },
        organization: o ? { id: o.id, name: o.name, slug: o.slug, role: o.role } : null,
        sessionId: s.session.id,
        expiresAt: s.session.expiresAt,
        via: s.via,
    };
}
/** A path on this site (for ?next=), or undefined: never another origin. */
export function localPath(p) {
    if (typeof p !== "string" || !p.startsWith("/") || p.startsWith("//") || p.startsWith("/\\") || /[\u0000-\u001f]/.test(p))
        return undefined;
    return p;
}
/** "/dashboard/:path*" → a RegExp (Next.js matcher syntax: :name, :name?, :name+, :name*). */
export function pattern(p) {
    let re = "";
    for (const seg of p.split("/").filter(Boolean)) {
        const m = /^:\w+([?+*]?)$/.exec(seg);
        if (!m)
            re += "/" + seg.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
        else if (m[1] === "")
            re += "/[^/]+";
        else
            re += `(?:/[^/]+)${m[1]}`;
    }
    return new RegExp(`^${re}/?$`);
}
