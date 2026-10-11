import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight, Fingerprint } from "lucide-react";
import { useEffect, useState } from "react";
import { aq } from "@/api/access";
import { AuthFrame } from "@/components/auth-frame";
import { deviceName, PasskeyConfirm, usePasskeySetup } from "@/components/passkey-setup";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { useMe } from "@/lib/me";
import { passkeyPlace, passkeyWords, webauthnSupported } from "@/lib/webauthn";

/**
 * Right after signing in with a link, for someone with no passkey yet: one
 * step that offers one, so the next sign-in doesn't need a link at all. Then
 * on to `next`. "Not now" goes straight on; the dashboard's banner keeps
 * reminding while there's no way back in. Success shows only once the box
 * has saved the passkey.
 */
export function WelcomePasskeyPage({ next }: { next?: string }) {
  const navigate = useNavigate();
  const words = passkeyWords();
  const to = next ?? "/";
  const recovery = useQuery(aq.recovery);
  const box = useQuery(aq.box);
  const { role } = useMe();
  const setup = usePasskeySetup();
  const [done, setDone] = useState(false);

  useEffect(() => {
    document.title = "Your next sign-in · Tiffin";
  }, []);

  // Nothing to offer: a browser that can't make passkeys, or someone who already has one.
  const skip = !webauthnSupported() || (!done && !setup.busy && (recovery.data?.passkeys ?? 0) > 0);
  useEffect(() => {
    if (skip) void navigate({ href: to, replace: true });
  }, [skip, navigate, to]);

  const add = async () => {
    if (await setup.add(deviceName())) setDone(true);
  };
  // An SSH key is the second way in on a box ShipTiffin set up, and only the owner adds one.
  const secondWay = done && role === "owner" && !!box.data?.managed && recovery.data?.sshKeys === 0;
  const back = `/welcome/passkey${to !== "/" ? `?next=${encodeURIComponent(to)}` : ""}`;

  if (skip) return <AuthFrame>{null}</AuthFrame>;
  return (
    <AuthFrame lit={done}>
      <h1 key={String(done)} className="sentence animate-rise text-ink" aria-live="polite">
        {done ? `Done. Next time, sign in with ${words.button}.` : `Next time, sign in with ${words.button}.`}
      </h1>

      {!done && (
        <div className="animate-rise" style={{ animationDelay: "60ms" }}>
          <p className="mt-2.5 text-md text-ink-2">{passkeyPlace()}</p>
          <div className="mt-6 flex flex-wrap items-center gap-x-5 gap-y-3">
            <Button variant="primary" size="lg" onClick={add} disabled={setup.busy}>
              <Fingerprint />
              {setup.busy ? `Waiting for ${words.button}…` : `Add ${words.button}`}
            </Button>
            <button
              type="button"
              onClick={() => void navigate({ href: to, replace: true })}
              className="text-[0.875rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink"
            >
              Not now
            </button>
          </div>
          {setup.cancelled && (
            <p role="status" className="mt-4 text-[0.875rem] text-ink-3">
              Nothing was saved. Try again when you’re ready.
            </p>
          )}
          {setup.error && !setup.cancelled && <ProblemNote className="mt-4" error={new Error(setup.error)} />}
        </div>
      )}

      {done && (
        <div className="animate-rise" style={{ animationDelay: "60ms" }}>
          <Button variant="primary" size="lg" className="mt-6" onClick={() => void navigate({ href: to, replace: true })}>
            Continue <ArrowRight />
          </Button>
          {secondWay && (
            <p className="mt-5 text-[0.875rem] text-ink-3">
              <Link to="/settings/server-access" className="text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink">
                Add a second way in
              </Link>
              , in case this device is ever lost.
            </p>
          )}
        </div>
      )}

      <PasskeyConfirm
        state={setup.confirm}
        onClose={() => setup.setConfirm(null)}
        onConfirmed={() => setup.setConfirm("done")}
        retry={() => void add()}
        again={`Add ${words.button}`}
        back={back}
      />
    </AuthFrame>
  );
}
