import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, Check } from "lucide-react";
import { Link } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { RecordsTable } from "@/components/dns-records";
import { kindOf } from "@/components/domains-parts";
import { DomainSetup } from "@/components/domains-setup";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Checkbox, Select } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { useDebounced } from "@/lib/debounced";
import { addDomain, cleanDomain, looksLikeDomain, ownedDomainsQuery, planAddDomain, type BoxDomain, type DnsRecord, type Domain } from "@/lib/domains";
import { undoChange } from "@/lib/staged";

type Props = {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  project: string;
  /** The project's web apps; the first choice is "web" when there is one. */
  apps: string[];
  /** Names already served here. */
  taken: string[];
  box?: BoxDomain;
  /** The project's domains as the page has them (refreshing while one waits). */
  list: Domain[];
  local: boolean;
  /** Closed after adding: the page opens that domain's row. */
  onDone: (domain: string) => void;
};

/**
 * Add a domain in two steps, like the big hosts: the name (with what kind of
 * name it is and what that means for its records), then the exact records
 * to set, watched live until the domain is up. For one you already own;
 * Domains (the sidebar) buys new ones.
 */
export function AddDomainDialog(props: Props) {
  const { open, onOpenChange } = props;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[42rem]">{open && <Flow {...props} />}</DialogContent>
    </Dialog>
  );
}

/** ShipTiffin's own domain: managed boxes get names under it, and nobody else can set its DNS. */
const SHARED = "shiptiffin.app";

function Flow({ onOpenChange, project, apps, taken, box, list, local, onDone }: Props) {
  const qc = useQueryClient();
  const [raw, setRaw] = useState("");
  const [app, setApp] = useState("");
  const [www, setWww] = useState(true);
  const [added, setAdded] = useState<Domain | null>(null);
  const domain = cleanDomain(raw);
  const chosen = app || (apps.includes("web") ? "web" : apps[0]) || "";
  const valid = looksLikeDomain(domain);
  const kind = kindOf(domain);
  const apex = valid && kind === "apex";
  const dup = taken.includes(domain);
  // A name one level under the apps domain is the box's own: it already points here, so it is added with nothing to set.
  // Deeper names, and the dashboard's, can't be an app's. Other names under ShipTiffin's own domain belong to other boxes.
  const under = !!box?.appsDomain && domain.endsWith(`.${box.appsDomain}`);
  const label = domain.split(".")[0];
  const freeName = under && !domain.slice(0, -box!.appsDomain.length - 1).includes(".") && domain !== box!.dashboard;
  const boxName = !!box && (domain === box.dashboard || (under && !freeName));
  const shared = !under && (domain === SHARED || domain.endsWith(`.${SHARED}`));
  const ips = box?.publicIps ?? [];
  // Domains the box can manage that no project uses yet: one click fills the name.
  const owned = useQuery({ ...ownedDomainsQuery, enabled: !local });
  const picks = (owned.data?.domains ?? []).filter((o) => o.manageable && !o.box && !o.usedBy?.length && !taken.includes(o.domain)).map((o) => o.domain);
  const problem = raw.trim() === "" ? null : !valid ? "bad" : dup ? "dup" : boxName ? "box" : shared ? "shared" : null;

  // What adding it would do, asked of the box without changing anything: the plan, and the exact records to set.
  const body = { domain, app: chosen, www: apex && www };
  const asked = useDebounced(JSON.stringify(body), 350);
  const preview = useQuery({
    queryKey: ["domain-plan", project, asked],
    queryFn: () => planAddDomain(project, JSON.parse(asked) as typeof body),
    enabled: asked === JSON.stringify(body) && valid && !!chosen && !problem && !freeName,
    retry: false,
    staleTime: 30_000,
  });

  const add = useMutation({
    mutationFn: () => addDomain(project, body, preview.data?.hash),
    onSuccess: (r) => {
      setAdded(r.domain ?? ({ domain, state: "waiting_for_dns", routes: [{ path: "/", app: chosen }] } as Domain));
      void qc.invalidateQueries({ queryKey: ["domains", project] });
      for (const k of [["manifest", project], ["project", project], ["changes"]]) void qc.invalidateQueries({ queryKey: k });
      const id = r.change?.id;
      toast({
        title: `Added ${domain} to ${project}.`,
        action: id ? { label: "Undo", run: () => undoChange(id).then(() => qc.invalidateQueries({ queryKey: ["domains", project] })) } : undefined,
      });
    },
  });

  if (added) {
    const d = list.find((x) => x.domain === added.domain) ?? added;
    const w = list.find((x) => x.redirectTo === added.domain);
    const live = d.state === "live" && (!w || w.state === "live");
    const close = () => {
      onOpenChange(false);
      onDone(d.domain);
    };
    return (
      <>
        <DialogHeader>
          <DialogTitle>{live ? `${d.domain} is live` : `Point ${d.domain} at your box`}</DialogTitle>
          <DialogDescription>
            {live
              ? `It serves ${chosen} over HTTPS. The certificate renews itself.`
              : local
                ? `Added to ${project}. It starts working once the box runs on a server.`
                : `Added to ${project}, served by ${chosen}. Set ${w ? "these records" : "the record below"} where you manage the domain’s DNS; this window watches for them.`}
          </DialogDescription>
        </DialogHeader>
        <DialogBody>
          {live ? (
            <div className="flex items-center gap-3 rounded-[10px] bg-ok-wash px-4 py-3.5">
              <span className="grid size-7 shrink-0 place-items-center rounded-full bg-ok text-paper">
                <Check className="size-4" strokeWidth={3} />
              </span>
              <a href={d.url} target="_blank" rel="noopener noreferrer" className="group ident inline-flex min-w-0 items-center gap-1 text-[0.875rem] text-ink hover:text-brass-ink">
                <span className="truncate">{d.url.replace(/^https?:\/\//, "")}</span>
                <ArrowUpRight className="size-3.5 shrink-0 text-ink-3 group-hover:text-brass-ink" />
              </a>
            </div>
          ) : (
            <DomainSetup project={project} d={d} www={w} writer local={local} />
          )}
        </DialogBody>
        <DialogFooter>
          {!live && <span className="mr-auto text-[0.8125rem] text-ink-3 max-sm:text-center">You can close this. The box keeps checking.</span>}
          <Button variant={live ? "primary" : "secondary"} onClick={close} autoFocus>
            {live ? "Done" : "Close"}
          </Button>
        </DialogFooter>
      </>
    );
  }

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (valid && chosen && !dup && !boxName && !shared && !add.isPending) add.mutate();
  };
  return (
    <form onSubmit={submit} className="contents">
      <DialogHeader>
        <DialogTitle>Add a domain</DialogTitle>
        <DialogDescription>
          {box?.appsDomain ? <>A free name on your box (blog.{box.appsDomain}) works at once. </> : null}
          For a domain you own, you’ll get the exact DNS records to set, and HTTPS follows by itself.
        </DialogDescription>
      </DialogHeader>
      <DialogBody className="space-y-5">
        <div>
          <label htmlFor="add-domain" className="text-[0.8125rem] font-[550] text-ink">
            Domain
          </label>
          <Input
            id="add-domain"
            value={raw}
            onChange={(e) => setRaw(e.target.value)}
            placeholder={`${project}.com`}
            autoComplete="off"
            autoCapitalize="off"
            spellCheck={false}
            autoFocus
            aria-invalid={!!problem || undefined}
            aria-describedby="add-domain-note"
            className="ident mt-1.5 text-[0.875rem]"
          />
          {raw.trim() === "" && picks.length > 0 && (
            <div className="mt-2.5 flex flex-wrap items-center gap-1.5 text-[0.8125rem]">
              <span className="text-ink-3">Yours:</span>
              {picks.slice(0, 6).map((d) => (
                <button key={d} type="button" onClick={() => setRaw(d)} className="ident rounded-[6px] border border-rule-2 bg-paper-raised px-2 py-0.5 text-[0.75rem] text-ink-2 hover:border-rule-3 hover:text-ink">
                  {d}
                </button>
              ))}
            </div>
          )}
          <div id="add-domain-note" className="mt-2 text-[0.8125rem] empty:hidden">
            {problem === "bad" ? (
              <span className="text-danger">That doesn’t look like a domain. Use a name like {project}.com or shop.example.com.</span>
            ) : problem === "dup" ? (
              <span className="text-ink-2">{domain} is already on this page.</span>
            ) : problem === "box" ? (
              <span className="text-ink-2">That name can’t be an app’s. Use one name before {box?.appsDomain}, like {label}.{box?.appsDomain}.</span>
            ) : problem === "shared" ? (
              <span className="text-ink-2">
                Names under {SHARED} belong to ShipTiffin, so their DNS can’t be set for this box.{" "}
                {box?.appsDomain && (
                  <>
                    Use{" "}
                    <button type="button" className="text-ink underline decoration-rule-3 underline-offset-2 hover:decoration-current" onClick={() => setRaw(`${label}.${box.appsDomain}`)}>
                      {label}.{box.appsDomain}
                    </button>{" "}
                    instead: it’s free and works at once.
                  </>
                )}
              </span>
            ) : freeName ? (
              <span className="text-ink-2">A name on your box: it already points here, so there’s no DNS to set.</span>
            ) : !valid ? (
              <span className="text-ink-3">A whole domain (example.com) or a subdomain (shop.example.com).</span>
            ) : null}
          </div>
          {valid && !problem && !freeName && <Kind domain={domain} ips={ips} local={local} records={preview.data?.records} />}
        </div>

        {apps.length > 1 ? (
          <div>
            <label htmlFor="add-domain-app" className="text-[0.8125rem] font-[550] text-ink">
              Served by
            </label>
            <div className="mt-1.5 max-w-[16rem]">
              <Select id="add-domain-app" value={chosen} onValueChange={setApp} options={apps.map((a) => ({ value: a, label: a }))} />
            </div>
            <p className="mt-1.5 text-[0.8125rem] text-ink-3">The app visitors see. You can change it later, or send one path to another app.</p>
          </div>
        ) : chosen ? (
          <p className="text-[0.8125rem] text-ink-3">
            It will show <span className="ident text-[0.75rem] text-ink-2">{chosen}</span>, the project’s web app.
          </p>
        ) : null}

        {apex && (
          <label className="flex cursor-pointer items-start gap-2.5">
            <Checkbox checked={www} onCheckedChange={(v) => setWww(v === true)} className="mt-0.5" />
            <span className="text-[0.875rem] text-ink">
              Redirect <span className="ident text-[0.8125rem]">www.{domain}</span> to <span className="ident text-[0.8125rem]">{domain}</span>
              <span className="block text-[0.8125rem] text-ink-3">Recommended. People who type www land on the same site. It needs one record of its own.</span>
            </span>
          </label>
        )}

        {add.isError && <ProblemNote error={add.error} />}
      </DialogBody>
      <DialogFooter>
        <span className="mr-auto text-[0.8125rem] text-ink-3 max-sm:text-center">
          Need one?{" "}
          <Link to="/domains" search={{ project, q: raw.trim() || undefined }} className="text-ink underline decoration-rule-3 underline-offset-2 hover:decoration-current">
            Buy a domain
          </Link>
        </span>
        <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={!valid || !chosen || !!problem || add.isPending}>
          {add.isPending ? "Adding…" : "Add domain"}
        </Button>
      </DialogFooter>
    </form>
  );
}

/** What kind of name it is, and the exact records it will need (from the box's plan, before anything changes). */
function Kind({ domain, ips, local, records }: { domain: string; ips: string[]; local: boolean; records?: DnsRecord[] }) {
  const apex = kindOf(domain) === "apex";
  const v4 = ips.filter((a) => !a.includes(":"));
  const rest = domain.split(".").slice(1).join(".");
  return (
    <div className="mt-3 rounded-[8px] bg-paper-sunk px-3.5 py-3 text-[0.8125rem] leading-5">
      <p className="font-[550] text-ink">{apex ? "A whole domain" : "A subdomain"}</p>
      <p className="mt-0.5 text-ink-2">
        {local
          ? "It will need DNS records pointing at your box once the box runs on a server."
          : records?.length
            ? `Once you add it, set ${records.length === 1 ? "this record" : `these ${records.length} records`} where you manage its DNS. You can set them now.${apex ? "" : ` The rest of ${rest} stays where it is.`}`
            : apex
              ? `It points at your box with ${v4.length === 1 ? "an A record" : "A records"}. Most DNS hosts don’t allow a CNAME on a whole domain.`
              : `It points at your box with its own records. The rest of ${rest} stays where it is.`}
      </p>
      {!local && !!records?.length && <RecordsTable className="mt-2.5" records={records} />}
    </div>
  );
}
