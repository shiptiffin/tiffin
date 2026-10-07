import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { methodWords, sessions, sq, type Session } from "@/api/sessions";
import { Confirm } from "@/components/confirm";
import { useTitle } from "@/components/favicon";
import { accessCrumbs, Group } from "@/components/health-kit";
import { StateSentence } from "@/components/jobs-words";
import { Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { DeviceIcon, Dots, SessionRows, whereWords } from "@/components/sessions-list";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { countWords } from "@/lib/format";
import { useMe } from "@/lib/me";
import { full, relative } from "@/lib/time";
import { passkeyWords } from "@/lib/webauthn";

/**
 * Settings › Sign-ins: where you're signed in to this dashboard (each
 * browser, where and how, last active), Sign out on any of them or
 * everywhere else, the last 30 days of sign-ins, and the browsers the box
 * recognises (no new sign-in email from those). The new sign-in email's
 * button opens this page. API keys are not sign-ins; they have their own page.
 */
export function SignInsPage() {
  useTitle("Sign-ins");
  const qc = useQueryClient();
  const all = useQuery(sq.sessions());
  const browsers = useQuery(sq.browsers());
  const { admin } = useMe();
  const [endingOthers, setEndingOthers] = useState(false);

  const list = all.data ?? [];
  const open = list.filter((s) => s.state === "active");
  const others = open.filter((s) => !s.current);

  return (
    <Page>
      <PageHeader
        eyebrow={accessCrumbs}
        title="Sign-ins"
        lede="Where you’re signed in to this dashboard. Each sign-in lasts 12 hours. If you see one you don’t recognise, sign it out."
        actions={
          others.length > 0 && (
            <Button variant="secondary" size="lg" onClick={() => setEndingOthers(true)}>
              Sign out everywhere else
            </Button>
          )
        }
      />

      {all.isError && <ProblemNote className="mt-8" error={all.error} title="Couldn’t load your sign-ins." />}

      {all.isSuccess && (
        <StateSentence className="mt-6">
          {others.length === 0
            ? "Only this browser is signed in."
            : `You’re signed in on ${countWords(open.length, "browser")}: this one and ${countWords(others.length, "other")}.`}
        </StateSentence>
      )}

      <Group label="Where you’re signed in" id="open">
        {all.isPending && <Skeleton className="h-36" />}
        {all.isSuccess && open.length > 0 && <SessionRows list={open} />}
        {all.isSuccess && open.length === 0 && <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">No open sessions.</p>}
      </Group>

      <Group label="Recent sign-ins" id="recent" aside="Last 30 days">
        {all.isPending && <Skeleton className="h-28" />}
        {all.isSuccess && <History list={list} />}
      </Group>

      <Group label="Browsers this box knows" id="browsers" aside={browsers.data?.length ? countWords(browsers.data.length, "browser") : undefined}>
        <p className="mb-3 max-w-[40rem] text-[0.875rem] text-ink-2">
          Signing in from one of these is quiet. From any other browser, the box emails you about it, if it has your email address.
        </p>
        {browsers.isError && <ProblemNote error={browsers.error} />}
        {browsers.isPending && <Skeleton className="h-20" />}
        {browsers.isSuccess && browsers.data.length === 0 && <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">None yet.</p>}
        {browsers.isSuccess && browsers.data.length > 0 && (
          <ul className="divide-y divide-rule border-y border-rule">
            {browsers.data.map((b, i) => (
              <li key={i} className="grid grid-cols-[1.5rem_minmax(0,1fr)] items-start gap-x-3 py-3 sm:grid-cols-[1.5rem_minmax(0,1fr)_auto] sm:items-center">
                <span className="flex h-5 items-center sm:h-auto">
                  <DeviceIcon device={b.device} />
                </span>
                <p className="min-w-0 text-[0.875rem] text-ink">
                  {b.device}
                  {b.current && <span className="ml-2 text-[0.8125rem] font-[550] text-brass-ink">This browser</span>}
                </p>
                <p className="col-start-2 text-[0.8125rem] text-ink-3 sm:col-start-3" title={`First seen ${full(b.firstSeen)}`}>
                  Last sign-in {relative(b.lastSeen)}
                </p>
              </li>
            ))}
          </ul>
        )}
      </Group>

      <Group label="Other ways in" id="other">
        <ul className="divide-y divide-rule border-y border-rule text-[0.875rem]">
          <li className="flex flex-col gap-0.5 py-3 sm:flex-row sm:items-baseline sm:justify-between sm:gap-6">
            <span className="text-ink-2">{passkeyWords().name.replace(/^./, (c) => c.toUpperCase())} on your devices can sign you in without a link.</span>
            <Link to="/settings/passkeys" className="shrink-0 font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
              Passkeys
            </Link>
          </li>
          <li className="flex flex-col gap-0.5 py-3 sm:flex-row sm:items-baseline sm:justify-between sm:gap-6">
            <span className="text-ink-2">
              Agents and scripts use API keys, not sign-ins, so they aren’t listed here, and signing out doesn’t stop them.{admin ? "" : " Owners and admins manage them."}
            </span>
            {admin && (
              <Link to="/settings/keys" search={{}} className="shrink-0 font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
                API keys
              </Link>
            )}
          </li>
        </ul>
      </Group>

      <Confirm
        open={endingOthers}
        onClose={() => setEndingOthers(false)}
        title="Sign out everywhere else?"
        body={`${countWords(others.length, "other browser", "other browsers", true)} ${others.length === 1 ? "is" : "are"} signed out at once; this one stays. API keys keep working; revoke them on the API keys page.`}
        action="Sign out everywhere else"
        tone="normal"
        cancel="Cancel"
        run={() => sessions.endOthers()}
        done={() => {
          void qc.invalidateQueries({ queryKey: ["sessions"] });
          toast({ title: `Signed out ${countWords(others.length, "other browser")}.` });
        }}
      />
    </Page>
  );
}

const stateWords: Record<Session["state"], string> = { active: "Signed in", ended: "Signed out", expired: "Expired" };

/** Every sign-in of the last 30 days, newest first: when, how, browser, where, and what became of it. */
function History({ list }: { list: Session[] }) {
  const rows = [...list].sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  if (rows.length === 0) return <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">No sign-ins in the last 30 days.</p>;
  return (
    <div className="border-y border-rule">
      <table className="w-full text-left text-[0.875rem] max-sm:block">
        <thead className="max-sm:hidden">
          <tr className="border-b border-rule text-[0.8125rem] text-ink-3">
            <th className="py-2 pr-4 font-normal">When</th>
            <th className="py-2 pr-4 font-normal">How</th>
            <th className="py-2 pr-4 font-normal">Browser</th>
            <th className="py-2 pr-4 font-normal">Where</th>
            <th className="py-2 text-right font-normal">Status</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-rule max-sm:block">
          {rows.map((s) => (
            <tr key={s.id} className="max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:py-3">
              <td className="py-2.5 pr-4 whitespace-nowrap text-ink max-sm:p-0" title={full(s.createdAt)}>
                {relative(s.createdAt)}
              </td>
              <td className="py-2.5 pr-4 text-ink-2 max-sm:hidden">{methodWords[s.method] ?? "—"}</td>
              <td className="py-2.5 pr-4 text-ink-2 max-sm:hidden">{s.device}</td>
              <td className="py-2.5 pr-4 text-ink-2 max-sm:hidden">{whereWords(s) || "—"}</td>
              <td
                className={cn(
                  "py-2.5 text-right whitespace-nowrap max-sm:p-0",
                  s.state === "active" ? "text-ink" : "text-ink-3",
                )}
              >
                {s.current ? "This browser" : stateWords[s.state]}
              </td>
              <td className="col-span-2 hidden max-sm:block">
                <Dots className="mt-0.5 text-[0.8125rem] text-ink-3" parts={[methodWords[s.method], s.device, whereWords(s)]} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
