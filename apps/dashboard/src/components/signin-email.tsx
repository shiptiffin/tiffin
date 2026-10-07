import { MailCheck } from "lucide-react";
import { useActionState, useState } from "react";
import { boxMail } from "@/api/modules";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";

// "Email me a sign-in link" on the login page. The box answers the same
// way whether or not the address belongs to anyone, so the page does too.

type Sent = { to: string; minutes: number };
type State = { sent?: Sent; error?: unknown };

export function EmailSignIn({ primary, className }: { primary: boolean; className?: string }) {
  const [value, setValue] = useState("");
  const [state, send, pending] = useActionState<State, FormData>(async (_prev, fd) => {
    const to = String(fd.get("email") ?? "").trim();
    try {
      const r = await boxMail.emailSignIn(to);
      return { sent: { to, minutes: r.expiresInMinutes } };
    } catch (error) {
      return { error };
    }
  }, {});
  const [again, setAgain] = useState(false);

  if (state.sent && !again)
    return (
      <div className={cn("animate-rise", className)} aria-live="polite">
        <p className="flex items-start gap-2.5 text-md text-ink">
          <MailCheck className="mt-[3px] size-[18px] shrink-0 text-ok" aria-hidden />
          <span>Check your inbox.</span>
        </p>
        <p className="mt-1.5 pl-[28px] text-[0.875rem] text-ink-2">
          If <span className="font-[550] text-ink [overflow-wrap:anywhere]">{state.sent.to}</span> belongs to someone on this box, a sign-in link is on its way. It
          works once, within {state.sent.minutes} minutes.
        </p>
        <button
          type="button"
          onClick={() => setAgain(true)}
          className="mt-3 ml-[28px] text-[0.875rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink"
        >
          Use another address
        </button>
      </div>
    );

  return (
    <form
      action={(fd) => {
        setAgain(false);
        send(fd);
      }}
      className={className}
    >
      <label htmlFor="signin-email" className={cn("block text-ink-2", primary ? "text-md" : "text-[0.875rem]")}>
        {primary ? "Enter your email and the box sends you a sign-in link." : "Or get a sign-in link by email."}
      </label>
      <div className="mt-3 flex gap-2 max-[389px]:flex-col">
        <Input
          id="signin-email"
          name="email"
          type="email"
          required
          autoComplete="email"
          inputMode="email"
          spellCheck={false}
          placeholder="you@example.com"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          className={primary ? "h-[38px]" : undefined}
        />
        <Button type="submit" variant={primary ? "primary" : "secondary"} size={primary ? "lg" : "md"} disabled={pending} className={primary ? "shrink-0" : "h-9 shrink-0"}>
          {pending ? "Sending…" : "Email me a link"}
        </Button>
      </div>
      {!!state.error && <ProblemNote className="mt-3" error={state.error} />}
    </form>
  );
}
