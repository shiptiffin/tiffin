import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError, api, isProblem } from "@/api/client";
import { ConfirmItsYou } from "@/components/confirm-its-you";
import { sentence } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { createCredential, passkeyError, passkeyWords } from "@/lib/webauthn";

/** What to call this device until the person names it: "Mac", "iPhone", "Windows PC". */
export function deviceName() {
  const ua = navigator.userAgent;
  return /Mac/.test(ua) ? "Mac" : /iPhone|iPad/.test(ua) ? "iPhone" : /Android/.test(ua) ? "Android phone" : /Windows/.test(ua) ? "Windows PC" : "This device";
}

/**
 * Adding a passkey on this device: options from the box → the browser's
 * prompt → the box stores it. Shared by Settings › Touch ID / Face ID and the
 * first sign-in step. `add` resolves true only once the box has saved it.
 * A passkey signs in for good, so the box may want a recent sign-in first
 * (reauth_required): `confirm` turns "ask", and <PasskeyConfirm> asks.
 * Closing the browser's prompt sets `cancelled`, so a page can say it calmly.
 */
export function usePasskeySetup() {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [cancelled, setCancelled] = useState(false);
  const [confirm, setConfirm] = useState<"ask" | "done" | null>(null);

  const add = async (name: string): Promise<boolean> => {
    setBusy(true);
    setError(null);
    setCancelled(false);
    try {
      const options = await api.beginPasskey();
      const credential = await createCredential(options);
      await api.addPasskey(name.trim() || deviceName(), credential);
      void qc.invalidateQueries({ queryKey: ["passkeys"] });
      void qc.invalidateQueries({ queryKey: ["recovery"] });
      return true;
    } catch (e) {
      if (isProblem(e, "reauth_required")) setConfirm("ask");
      else {
        setCancelled(e instanceof DOMException && (e.name === "NotAllowedError" || e.name === "AbortError"));
        setError(e instanceof ApiError ? sentence(e.problem.detail ?? e.message) : passkeyError(e));
      }
      return false;
    } finally {
      setBusy(false);
    }
  };

  return { add, busy, error, cancelled, confirm, setConfirm };
}

/**
 * "Confirm it's you" for adding a passkey, then "It's you" with the button
 * that tries again (`retry`, labelled `again`). `back` is where "Sign in
 * again" returns to.
 */
export function PasskeyConfirm({
  state,
  onClose,
  onConfirmed,
  retry,
  again,
  back,
}: {
  state: "ask" | "done" | null;
  onClose: () => void;
  onConfirmed: () => void;
  retry: () => void;
  again: string;
  back: string;
}) {
  const words = passkeyWords();
  return (
    <Dialog open={!!state} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-md">
        {state === "ask" && (
          <ConfirmItsYou
            why="A passkey signs in as you from now on, so Tiffin checks it’s really you first: adding one needs a sign-in from the last 10 minutes."
            back={back}
            onBack={onClose}
            onConfirmed={onConfirmed}
            noPasskeyNote="With Google, GitHub or an emailed link you ask for; the owner can also use a fresh tiffin login. A link someone else made doesn’t count."
          />
        )}
        {state === "done" && (
          <>
            <DialogHeader>
              <DialogTitle>It’s you</DialogTitle>
              <DialogDescription>For the next 10 minutes you can add passkeys. Your browser asks for {words.how} next.</DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button
                variant="primary"
                onClick={() => {
                  onClose();
                  retry();
                }}
              >
                {again}
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
