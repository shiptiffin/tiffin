// WebAuthn glue: the box (go-webauthn) speaks JSON with base64url byte
// fields; the browser API wants ArrayBuffers. These convert both ways.

const b64urlToBuf = (s: string): ArrayBuffer => {
  const b64 = s
    .replace(/-/g, "+")
    .replace(/_/g, "/")
    .padEnd(Math.ceil(s.length / 4) * 4, "=");
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
};

const bufToB64url = (b: ArrayBuffer | null | undefined): string | undefined => {
  if (!b) return undefined;
  const bytes = new Uint8Array(b);
  let bin = "";
  for (const x of bytes) bin += String.fromCharCode(x);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
};

type Json = Record<string, unknown>;
type Desc = { id: string; type: string; transports?: string[] };

export function webauthnSupported() {
  return typeof window !== "undefined" && !!window.PublicKeyCredential && window.isSecureContext;
}

/**
 * Passkey sign-in in this device's own words. title: a page or menu title;
 * name: after "Sign in with"; button: the same, shorter where the device has
 * one way (a Mac's Touch ID); how: what the browser will ask for.
 */
export type PasskeyWords = { title: string; name: string; button: string; how: string };

export function passkeyWords(): PasskeyWords {
  const nav = typeof navigator === "undefined" ? undefined : (navigator as Navigator & { userAgentData?: { platform?: string } });
  const p = [nav?.userAgentData?.platform, nav?.platform, nav?.userAgent].join(" ");
  if (/android/i.test(p)) return { title: "Fingerprint or face", name: "fingerprint or face", button: "fingerprint or face", how: "your fingerprint or face" };
  if (/mac|iphone|ipad|ipod/i.test(p)) {
    // An iPad asking for the desktop site says Macintosh, but has a touch screen.
    const mac = !/iphone|ipad|ipod/i.test(p) && (nav?.maxTouchPoints ?? 0) < 2;
    return { title: "Touch ID / Face ID", name: "Touch ID / Face ID", button: mac ? "Touch ID" : "Touch ID / Face ID", how: "your fingerprint or face" };
  }
  if (/windows|win32|win64/i.test(p)) return { title: "Windows Hello", name: "Windows Hello", button: "Windows Hello", how: "your face, fingerprint or PIN" };
  return { title: "Passkeys", name: "a passkey", button: "a passkey", how: "a passkey" };
}

/** Adds a passkey: creation options from the box → navigator.credentials.create() → JSON for the box. */
export async function createCredential(options: unknown): Promise<Json> {
  const pk = (options as { publicKey: Json }).publicKey;
  const user = pk.user as Json;
  const cred = (await navigator.credentials.create({
    publicKey: {
      ...(pk as object),
      challenge: b64urlToBuf(pk.challenge as string),
      user: { ...(user as object), id: b64urlToBuf(user.id as string) } as PublicKeyCredentialUserEntity,
      excludeCredentials: ((pk.excludeCredentials as Desc[] | undefined) ?? []).map((c) => ({ ...c, id: b64urlToBuf(c.id) })),
    } as PublicKeyCredentialCreationOptions,
  })) as PublicKeyCredential | null;
  if (!cred) throw new Error("No passkey was created.");
  const r = cred.response as AuthenticatorAttestationResponse;
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      clientDataJSON: bufToB64url(r.clientDataJSON),
      attestationObject: bufToB64url(r.attestationObject),
      transports: typeof r.getTransports === "function" ? r.getTransports() : undefined,
    },
  };
}

/** Signs with a passkey: assertion options from the box → navigator.credentials.get() → JSON for the box. */
export async function getAssertion(options: unknown): Promise<Json> {
  const pk = (options as { publicKey: Json }).publicKey;
  const cred = (await navigator.credentials.get({
    publicKey: {
      ...(pk as object),
      challenge: b64urlToBuf(pk.challenge as string),
      allowCredentials: ((pk.allowCredentials as Desc[] | undefined) ?? []).map((c) => ({ ...c, id: b64urlToBuf(c.id) })),
    } as PublicKeyCredentialRequestOptions,
  })) as PublicKeyCredential | null;
  if (!cred) throw new Error("No passkey answered.");
  const r = cred.response as AuthenticatorAssertionResponse;
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      clientDataJSON: bufToB64url(r.clientDataJSON),
      authenticatorData: bufToB64url(r.authenticatorData),
      signature: bufToB64url(r.signature),
      userHandle: bufToB64url(r.userHandle),
    },
  };
}

/** Turns WebAuthn DOMExceptions into plain words. */
export function passkeyError(e: unknown): string {
  if (e instanceof DOMException) {
    if (e.name === "NotAllowedError") return "The passkey prompt was closed or timed out. Nothing was approved.";
    if (e.name === "InvalidStateError") return "This passkey is already registered on this box.";
    if (e.name === "SecurityError") return "Passkeys need the dashboard's own address (HTTPS, or localhost). Open it from the link the box printed.";
  }
  return e instanceof Error ? e.message : "The passkey step failed.";
}
