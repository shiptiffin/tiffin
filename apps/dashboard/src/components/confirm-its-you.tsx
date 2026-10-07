import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Fingerprint } from "lucide-react";
import { useState, type ReactNode } from "react";
import { api } from "@/api/client";
import { q } from "@/api/queries";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { DialogBody, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { getAssertion, passkeyError, passkeyWords, webauthnSupported } from "@/lib/webauthn";

/**
 * Sudo mode, inside a dialog: some actions (an API key that outlives this
 * session, a new passkey) need proof the person is still here. The box says
 * reauth_required; this asks them to confirm with one of their passkeys (10
 * minutes, then onConfirmed runs) or to sign in again with a passkey, Google,
 * GitHub, an emailed link or (the owner) `tiffin login`, coming back to `back`.
 */
export function ConfirmItsYou({
  why,
  back,
  working,
  workingLabel,
  onBack,
  onConfirmed,
  noPasskeyNote,
}: {
  /** Why this needs it, one or two sentences. */
  why: ReactNode;
  /** Where "Sign in again" comes back to, e.g. "/settings/keys?create=true". */
  back: string;
  /** The action runs after confirming: "Creating…". */
  working?: boolean;
  workingLabel?: string;
  onBack: () => void;
  onConfirmed: () => void;
  /** Under the buttons when there's no passkey to confirm with. */
  noPasskeyNote?: string;
}) {
  const words = passkeyWords();
  const passkeys = useQuery({ ...q.passkeys, enabled: webauthnSupported(), retry: false });
  const canPasskey = webauthnSupported() && (passkeys.data?.length ?? 0) > 0;
  const [waiting, setWaiting] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const confirm = async () => {
    setError(null);
    setWaiting(true);
    try {
      await api.confirm(await getAssertion(await api.confirmOptions()));
      onConfirmed();
    } catch (e) {
      // Closing the prompt is a choice, not an error.
      if (!(e instanceof DOMException && (e.name === "NotAllowedError" || e.name === "AbortError"))) setError(e instanceof DOMException ? new Error(passkeyError(e)) : e);
    } finally {
      setWaiting(false);
    }
  };
  return (
    <>
      <DialogHeader>
        <DialogTitle>Confirm it’s you</DialogTitle>
        <DialogDescription>{why}</DialogDescription>
      </DialogHeader>
      <DialogBody className="flex flex-col gap-3">
        {canPasskey && (
          <Button variant="primary" size="lg" className="w-full" onClick={confirm} disabled={waiting || working}>
            <Fingerprint />
            {waiting ? `Waiting for ${words.button}…` : working ? (workingLabel ?? "Working…") : `Confirm with ${words.button}`}
          </Button>
        )}
        <Button asChild size="lg" variant={canPasskey ? "secondary" : "primary"} className="w-full">
          <Link to="/login" search={{ reason: "confirm", next: back }}>
            Sign in again
          </Link>
        </Button>
        <p className="text-xs text-ink-3">
          {canPasskey
            ? "Or sign in again with Google, GitHub or an emailed link, then come back here."
            : (noPasskeyNote ?? "With a passkey, Google, GitHub, an emailed link or, for the owner, a fresh tiffin login. Add a passkey in Settings › Passkeys to confirm in place next time.")}
        </p>
        {!!error && <ProblemNote error={error} />}
      </DialogBody>
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={onBack}>
          Back
        </Button>
      </DialogFooter>
    </>
  );
}
