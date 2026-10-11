import type { Recovery } from "@/api/access";

/** No passkey, no SSH key, no emailed link and no Google or GitHub: once this session ends, there's no way back in. */
export function noWayBack(r: Recovery | undefined): boolean {
  return !!r && r.passkeys === 0 && r.sshKeys === 0 && !r.email && r.providers === 0;
}

/**
 * Where a sign-in with a link goes next: someone with no passkey yet, on a
 * browser that can make one, first gets the step that offers it
 * (/welcome/passkey), which then goes on to `next`. Unknown (the box didn't
 * say) counts as having one, so nobody is held up.
 */
export function afterLinkSignIn(r: Recovery | undefined, canPasskey: boolean, next: string | undefined): string {
  const to = next ?? "/";
  if (!r || !canPasskey || r.passkeys > 0) return to;
  return `/welcome/passkey${to !== "/" ? `?next=${encodeURIComponent(to)}` : ""}`;
}
