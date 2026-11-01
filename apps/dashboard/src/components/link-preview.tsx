import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, Check, CircleAlert, RotateCw, TriangleAlert } from "lucide-react";
import { useEffect, useState } from "react";
import { ApiError } from "@/api/client";
import { mod3, type Deploy, type LinkPreview, type LinkPreviewCheck } from "@/api/modules";
import { CopyButton } from "@/components/copy";
import { inFlight, refreshApp } from "@/components/deploy-parts";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { ProjectIcon } from "@/components/project-icon";
import { Segmented } from "@/components/segmented";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { changeWords, comparedWords, crumb, shown, statusTone, summaryWords, tagsText } from "@/lib/link-preview";
import { full, relative } from "@/lib/time";

/** Whether a deploy gets a Link preview tab: a web app's deploy that went (or is going) live. */
export function hasLinkPreview(d: Deploy | undefined): boolean {
  if (!d || d.status === "failed" || d.status === "skipped") return false;
  return !!(d.appUrl || d.url || d.linkPreview);
}

/** A status mark: a quiet tick when fine, a coloured sign when not. */
export function StatusMark({ status, className }: { status: "ok" | "warn" | "bad" | "unreadable"; className?: string }) {
  if (status === "ok") return <Check aria-label="Fine" className={cn("size-3.5 shrink-0 text-ink-3", className)} />;
  if (status === "warn") return <TriangleAlert aria-label="Warning" className={cn("size-3.5 shrink-0 text-warn-ink", className)} />;
  return <CircleAlert aria-label="Problem" className={cn("size-3.5 shrink-0 text-danger", className)} />;
}

/** The deploy facts' "Link preview" value: a button to the tab. */
export function LinkPreviewFact({ d, onOpen }: { d: Deploy; onOpen: () => void }) {
  const lp = d.linkPreview!;
  return (
    <>
      <button
        type="button"
        onClick={onOpen}
        className={cn("inline-flex items-center gap-1.5 font-[550] underline decoration-rule-3 underline-offset-4 hover:decoration-ink", statusTone(lp.status) || "text-ink")}
      >
        {lp.status !== "ok" && <StatusMark status={lp.status} />}
        {summaryWords(lp)}
      </button>
      {lp.worse && <span className="block text-xs text-warn-ink">Worse than before</span>}
    </>
  );
}

type Surface = "google" | "x" | "linkedin" | "slack";

/** The Link preview tab: the shared link as each app draws it, what's wrong, what changed, the tags. */
export function LinkPreviewTab({ project, app, d, writer }: { project: string; app: string; d: Deploy; writer: boolean }) {
  const qc = useQueryClient();
  const key = ["link-preview", project, app, d.id];
  const waiting = inFlight(d.status);
  const q = useQuery({
    queryKey: key,
    // Just live: the box checks it in the background, so wait for that.
    queryFn: () => mod3.linkPreview(project, app, d.id, d.status === "live" && !d.linkPreview ? 15 : 0),
    enabled: !waiting,
    retry: false,
    staleTime: 60_000,
  });
  // A check the deploy record knows of that this report predates: read again.
  const seen = d.linkPreview?.checkedAt;
  const { data, refetch } = q;
  useEffect(() => {
    if (seen && data && seen > data.checkedAt) void refetch();
  }, [seen, data, refetch]);
  // The deploy record learns of a check that finished while this page was open.
  const missing = !!data && !d.linkPreview;
  useEffect(() => {
    if (missing) void qc.invalidateQueries({ queryKey: ["deploy", project, app, d.id] });
  }, [missing, qc, project, app, d.id]);

  const again = useMutation({
    mutationFn: () => mod3.checkLinkPreview(project, app, d.id),
    onSuccess: (r) => {
      qc.setQueryData(key, r);
      void qc.invalidateQueries({ queryKey: ["deploy", project, app, d.id] });
      refreshApp(qc, project, app);
    },
    onError: (e) => toast({ title: "Couldn’t check it again.", detail: e instanceof Error ? e.message : undefined, tone: "danger" }),
  });

  if (waiting) return <p className="py-6 text-sm text-ink-3">Checked once it’s live.</p>;
  if (q.isPending) return <Skeleton className="h-64" />;
  if (q.isError) {
    if (q.error instanceof ApiError && q.error.status === 404)
      return (
        <div className="max-w-[40rem] py-6">
          <p className="text-[0.9375rem] font-[550] text-ink">Not checked.</p>
          <p className="mt-1 text-sm text-ink-2">{q.error.problem.detail || "The box checks each version when it goes live; this one went live before it did."}</p>
        </div>
      );
    return <ProblemNote error={q.error} title="The link preview can’t be read." className="max-w-[46rem]" />;
  }
  const r = q.data;
  const canCheck = writer && r.canCheck;
  return (
    <div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 pb-4 text-[0.8125rem] text-ink-3">
        <span title={full(r.checkedAt)}>Checked {relative(r.checkedAt)}</span>
        <a href={r.url} target="_blank" rel="noopener noreferrer" className="ident inline-flex min-w-0 items-center gap-1 text-[0.75rem] text-brass-ink hover:text-ink">
          <span className="truncate">{r.url.replace(/^https?:\/\//, "")}</span>
          <ArrowUpRight className="size-3 shrink-0" aria-hidden />
        </a>
        {canCheck && (
          <Button size="sm" variant="ghost" className="ml-auto" disabled={again.isPending} onClick={() => again.mutate()}>
            <RotateCw className={cn(again.isPending && "animate-spin")} />
            {again.isPending ? "Checking…" : "Check again"}
          </Button>
        )}
      </div>

      {r.status === "unreadable" ? (
        <div className="max-w-[40rem] rounded-[10px] border border-danger-rule bg-danger-wash px-5 py-4">
          <p className="text-[0.9375rem] font-[550] text-ink">The box couldn’t read the home page.</p>
          <p className="mt-1 text-sm text-ink-2">{r.reason}</p>
        </div>
      ) : (
        <>
          <div className="grid items-start gap-x-10 gap-y-8 xl:grid-cols-[minmax(0,32rem)_minmax(0,1fr)]">
            <Cards project={project} r={r} />
            <Checklist checks={r.checks ?? []} />
          </div>
          <Changes r={r} />
        </>
      )}

      {(r.tags ?? []).length > 0 && <Tags tags={r.tags!} />}

      <p className="mt-6 max-w-[46rem] text-xs leading-5 text-ink-3">
        X, Facebook and LinkedIn keep their own copy of a page’s preview for days; this is what the page says now. To make them read it again, use{" "}
        <a href="https://developers.facebook.com/tools/debug/" target="_blank" rel="noopener noreferrer" className="text-ink-2 underline decoration-rule-3 underline-offset-2 hover:text-ink">
          Facebook’s Sharing Debugger
        </a>{" "}
        or{" "}
        <a href="https://www.linkedin.com/post-inspector/" target="_blank" rel="noopener noreferrer" className="text-ink-2 underline decoration-rule-3 underline-offset-2 hover:text-ink">
          LinkedIn’s Post Inspector
        </a>
        .
      </p>
    </div>
  );
}

// ------------------------------------------------------------------ the cards

function Cards({ project, r }: { project: string; r: LinkPreview }) {
  const [surface, setSurface] = useState<Surface>("x");
  const img = r.thumbUrl;
  const s = shown(r);
  return (
    <section aria-label="How a shared link looks" className="min-w-0">
      <Segmented<Surface>
        label="Where the link is shared"
        value={surface}
        onChange={setSurface}
        options={[
          { value: "x", label: "X" },
          { value: "linkedin", label: "LinkedIn" },
          { value: "slack", label: "Slack" },
          { value: "google", label: "Google" },
        ]}
      />
      <div className="mt-3 rounded-[12px] border border-rule bg-paper-sunk p-4 sm:p-5">
        {surface === "google" && <GoogleCard project={project} s={s} url={r.url} />}
        {surface === "x" && <XCard s={s} img={img} alt={r.image?.alt} />}
        {surface === "linkedin" && <LinkedInCard s={s} img={img} alt={r.image?.alt} />}
        {surface === "slack" && <SlackCard project={project} s={s} img={img} alt={r.image?.alt} />}
      </div>
      <p className="mt-2 text-xs text-ink-3">{surfaceNote[surface]}</p>
    </section>
  );
}

const surfaceNote: Record<Surface, string> = {
  x: "X reads twitter: tags first, then og: ones.",
  linkedin: "LinkedIn, Facebook and iMessage draw much the same card.",
  slack: "Slack, Discord and Teams unfurl links like this.",
  google: "Search results use the page’s <title> and meta description.",
};

type Shown = ReturnType<typeof shown>;

/** The share image at a card's width, 1.91:1 like the unfurlers crop it. */
function Wide({ img, alt, className }: { img: string; alt?: string; className?: string }) {
  return <img src={img} alt={alt ?? ""} className={cn("aspect-[1.91/1] w-full bg-paper-press object-cover", className)} />;
}

function GoogleCard({ project, s, url }: { project: string; s: Shown; url: string }) {
  return (
    <div className="max-w-[36rem]">
      <div className="flex items-center gap-2.5">
        <span className="grid size-7 shrink-0 place-items-center rounded-full border border-rule bg-paper-raised">
          <ProjectIcon project={project} size={16} />
        </span>
        <span className="min-w-0 leading-tight">
          <span className="block truncate text-[0.875rem] text-ink">{s.site}</span>
          <span className="block truncate text-xs text-ink-3">{crumb(url)}</span>
        </span>
      </div>
      <p className="mt-1.5 line-clamp-1 text-[1.25rem] leading-7 text-data-2">{s.searchTitle}</p>
      {s.searchText && <p className="mt-0.5 line-clamp-2 text-[0.875rem] leading-[1.4] text-ink-2">{s.searchText}</p>}
    </div>
  );
}

function XCard({ s, img, alt }: { s: Shown; img?: string; alt?: string }) {
  if (img && s.large)
    return (
      <div className="max-w-[32rem]">
        <div className="relative overflow-hidden rounded-[16px] border border-rule-2">
          <Wide img={img} alt={alt} />
          <span className="absolute bottom-3 left-3 max-w-[calc(100%-1.5rem)] truncate rounded-[4px] bg-[oklch(0_0_0/0.77)] px-1.5 py-0.5 text-[0.8125rem] text-white">{s.xTitle}</span>
        </div>
        <p className="mt-1 text-[0.8125rem] text-ink-3">From {s.host}</p>
      </div>
    );
  return (
    <div className="flex max-w-[32rem] overflow-hidden rounded-[16px] border border-rule-2 bg-paper-raised">
      {img && <img src={img} alt={alt ?? ""} className="aspect-square w-[7.5rem] shrink-0 border-r border-rule-2 bg-paper-press object-cover" />}
      <div className="flex min-w-0 flex-col justify-center gap-0.5 px-3 py-2.5 text-[0.875rem]">
        <span className="truncate text-ink-3">{s.host}</span>
        <span className="truncate text-ink">{s.xTitle}</span>
        {s.xText && <span className="line-clamp-2 text-ink-3">{s.xText}</span>}
      </div>
    </div>
  );
}

function LinkedInCard({ s, img, alt }: { s: Shown; img?: string; alt?: string }) {
  return (
    <div className="max-w-[34rem] overflow-hidden rounded-[8px] border border-rule-2 bg-paper-raised">
      {img && <Wide img={img} alt={alt} />}
      <div className="px-3 py-2.5">
        <p className="line-clamp-2 text-[0.875rem] leading-5 font-[600] text-ink">{s.title}</p>
        <p className="mt-0.5 truncate text-xs text-ink-3">{s.host}</p>
      </div>
    </div>
  );
}

function SlackCard({ project, s, img, alt }: { project: string; s: Shown; img?: string; alt?: string }) {
  return (
    <div className="flex max-w-[32rem] gap-3">
      <span className="w-1 shrink-0 rounded-full bg-rule-3" aria-hidden />
      <div className="min-w-0 py-0.5 text-[0.875rem] leading-[1.45]">
        <p className="flex items-center gap-1.5 font-[600] text-ink">
          <ProjectIcon project={project} size={16} />
          <span className="truncate">{s.site}</span>
        </p>
        <p className="font-[600] text-data-2">{s.title}</p>
        {s.text && <p className="text-ink-2">{s.text}</p>}
        {img && <img src={img} alt={alt ?? ""} className="mt-2 max-h-[13rem] max-w-full rounded-[8px] border border-rule bg-paper-press" />}
      </div>
    </div>
  );
}

// ------------------------------------------------------------------ checks, changes, tags

function Checklist({ checks }: { checks: LinkPreviewCheck[] }) {
  return (
    <section aria-labelledby="lp-checks" className="min-w-0">
      <h3 id="lp-checks" className="label mb-1.5">
        What the box checked
      </h3>
      <ul className="divide-y divide-rule border-y border-rule">
        {checks.map((c) => (
          <li key={c.id} className="grid grid-cols-[1rem_minmax(0,1fr)] gap-x-2.5 py-2.5">
            <StatusMark status={c.status} className="mt-[3px]" />
            <div className="min-w-0 text-[0.8125rem]">
              <p className={cn("font-[550]", statusTone(c.status) || "text-ink")}>{c.label}</p>
              <p className="break-words text-ink-2">{c.detail}</p>
              {c.status !== "ok" && c.fix && <p className="mt-0.5 text-ink-3">{c.fix}</p>}
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}

function Changes({ r }: { r: LinkPreview }) {
  const changes = r.changes ?? [];
  const title = comparedWords(r);
  if (!changes.length || !title) return null;
  return (
    <section aria-labelledby="lp-changes" className="mt-8 max-w-[46rem]">
      <h3 id="lp-changes" className="label mb-1.5">
        {title}
      </h3>
      <ul className="divide-y divide-rule border-y border-rule">
        {changes.map((c, i) => (
          <li key={i} className="flex items-start gap-2.5 py-2 text-[0.8125rem]">
            {c.worse ? <TriangleAlert aria-label="Worse" className="mt-[3px] size-3.5 shrink-0 text-warn-ink" /> : <span className="w-3.5 shrink-0" aria-hidden />}
            <span className={cn("min-w-0 break-words", c.worse ? "text-ink" : "text-ink-2")}>{changeWords(c)}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function Tags({ tags }: { tags: Array<{ name: string; value: string }> }) {
  return (
    <details className="group mt-8 max-w-[56rem]">
      <summary className="cursor-pointer list-none text-sm text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
        <span className="inline-block transition-transform group-open:rotate-90">›</span> The page’s tags ({tags.length})
      </summary>
      <div className="mt-2 rounded-[8px] border border-rule bg-paper-sunk">
        <div className="flex items-center justify-between border-b border-rule px-3 py-1.5">
          <span className="text-xs text-ink-3">As the page wrote them</span>
          <CopyButton value={tagsText(tags)} label="Copy the tags" className="size-6" />
        </div>
        <table className="w-full text-left">
          <tbody className="divide-y divide-rule">
            {tags.map((t, i) => (
              <tr key={i} className="align-top">
                <th scope="row" className="ident w-[14rem] px-3 py-1.5 text-[0.71875rem] font-[400] whitespace-nowrap text-ink-3 max-sm:w-[8rem] max-sm:whitespace-normal">
                  {t.name}
                </th>
                <td className="ident px-3 py-1.5 text-[0.71875rem] break-all text-ink">{t.value}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </details>
  );
}
