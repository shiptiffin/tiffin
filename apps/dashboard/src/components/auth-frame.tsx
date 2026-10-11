import type { ReactNode } from "react";
import { Wordmark } from "@/components/logo";
import { Mascot } from "@/components/mascot";

/**
 * The frame of the pages outside the dashboard (sign-in and the step right
 * after it): the mascot on its own plate, the words on the page. The
 * wordmark over the words is the name alone, so the tin shows once.
 */
export function AuthFrame({ lit, children }: { lit?: boolean; children: ReactNode }) {
  return (
    <div className="grid min-h-dvh bg-paper max-md:grid-rows-[auto_1fr] md:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)]">
      <aside
        aria-hidden
        className="relative flex items-center justify-center overflow-hidden border-rule bg-paper-sunk max-md:h-[13.5rem] max-md:border-b md:border-r"
      >
        <figure className="m-0 flex flex-col items-center md:-mt-6">
          {/* The mascot waits; once you're in it lights up (steam), cross-fading in place. */}
          <Mascot state={lit ? "live" : "base"} className="size-[168px] md:size-[min(26rem,36vw)]" />
          <figcaption className="mt-5 text-center text-[0.8125rem] leading-5 text-ink-3 max-md:hidden">
            Apps, database, auth, files, mail and jobs,
            <br />
            stacked in one machine you own.
          </figcaption>
        </figure>
      </aside>
      <div className="flex min-w-0 flex-col px-5 sm:px-12 lg:px-16">
        <header className="pt-6 md:pt-10">
          <Wordmark bare />
        </header>
        <main className="flex w-full max-w-[25rem] flex-1 flex-col justify-start pt-8 pb-10 md:justify-center md:py-12">{children}</main>
        <footer className="max-w-[25rem] pb-8 text-sm text-ink-3">Your whole app in one box. Your box, your data.</footer>
      </div>
    </div>
  );
}
