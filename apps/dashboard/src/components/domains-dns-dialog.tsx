import { useMutation } from "@tanstack/react-query";
import { ToggleGroup } from "radix-ui";
import { useRef, useState, type FormEvent } from "react";
import { dnsApi, type ZoneRecord } from "@/api/modules";
import { relName, toFullName } from "@/components/domains-parts";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/cn";

type Kind = "custom" | "google" | "challenge" | "spf" | "dmarc" | "caa";

/** Templates for the records people are usually asked to add. Each fills the form; nothing is sent until Save. */
const PRESETS: Record<Kind, { label: string; type: string; name: string; value: string; namePh?: string; valuePh: string; hint: string }> = {
  custom: { label: "Custom", type: "TXT", name: "", value: "", valuePh: "The value you were given", hint: "" },
  google: {
    label: "Google Search Console",
    type: "TXT",
    name: "@",
    value: "",
    valuePh: "google-site-verification=…",
    hint: "In Search Console, choose Domain, then copy the TXT value it shows. Paste all of it.",
  },
  challenge: {
    label: "Service challenge",
    type: "TXT",
    name: "",
    value: "",
    namePh: "_github-pages-challenge-you",
    valuePh: "The code the service shows",
    hint: "GitHub, Vercel, Netlify and others give a name (like _vercel or _github-pages-challenge-yourname) and a value. Paste both as given.",
  },
  spf: {
    label: "SPF",
    type: "TXT",
    name: "@",
    value: "",
    valuePh: "v=spf1 include:_spf.yourmailhost.com ~all",
    hint: "Says which servers may send mail as this domain. Your mail provider gives the include: part. A name has one SPF record: add to it rather than adding a second.",
  },
  dmarc: {
    label: "DMARC",
    type: "TXT",
    name: "_dmarc",
    value: "v=DMARC1; p=none",
    valuePh: "v=DMARC1; p=none",
    hint: "p=none only reports. Move to quarantine once your mail passes SPF and DKIM.",
  },
  caa: {
    label: "Allow Let’s Encrypt",
    type: "CAA",
    name: "@",
    value: '0 issue "letsencrypt.org"',
    valuePh: '0 issue "letsencrypt.org"',
    hint: "Only needed when the domain has other CAA records: your box’s certificates come from Let’s Encrypt.",
  },
};

const TYPES = ["TXT", "MX", "CAA", "CNAME", "A", "AAAA"];
const VALUE_HINT: Record<string, string> = {
  TXT: "Text, as given. No quotes needed.",
  MX: "Priority, then the mail server: 10 mx1.mailhost.com",
  CAA: 'Flag, tag and value: 0 issue "letsencrypt.org"',
  CNAME: "The name this one is an alias of, like target.example.net",
  A: "An IPv4 address, like 203.0.113.10",
  AAAA: "An IPv6 address, like 2001:db8::10",
};

/** Add or edit one record in a connected zone. */
export function DnsRecordDialog({
  open,
  onOpenChange,
  base,
  zone,
  records,
  editing,
  onDone,
  onEdit,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  /** The domain names are relative to ("@" is this). */
  base: string;
  zone: string;
  records: ZoneRecord[];
  editing?: ZoneRecord;
  onDone: () => void;
  /** Switch to editing an existing record (the SPF already there). */
  onEdit: (r: ZoneRecord) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[36rem]">
        {open && <Form base={base} zone={zone} records={records} editing={editing} onDone={onDone} onEdit={onEdit} close={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  );
}

function Form({ base, zone, records, editing, onDone, onEdit, close }: { base: string; zone: string; records: ZoneRecord[]; editing?: ZoneRecord; onDone: () => void; onEdit: (r: ZoneRecord) => void; close: () => void }) {
  const [kind, setKind] = useState<Kind>("custom");
  const [type, setType] = useState(editing?.type ?? "TXT");
  const [name, setName] = useState(editing ? relName(editing.name, base) : "");
  const [value, setValue] = useState(editing?.value ?? "");
  const preset = PRESETS[kind];
  const full = toFullName(name, base, zone);
  const v = value.trim();

  const nameRef = useRef<HTMLInputElement>(null);
  const valueRef = useRef<HTMLTextAreaElement & HTMLInputElement>(null);
  const pick = (k: Kind) => {
    const p = PRESETS[k];
    setKind(k);
    setType(p.type);
    setName(p.name);
    setValue(p.value);
    // Straight to the field that still needs typing.
    requestAnimationFrame(() => (p.name || k === "custom" ? valueRef : nameRef).current?.focus());
  };

  const same = (r: ZoneRecord) => r.name === full && r.type === type;
  const atName = records.filter((r) => r.name === full && !(editing && r === editing));
  const group = records.filter((r) => same(r) && r !== editing);
  const spfAt = group.find((r) => r.type === "TXT" && /^v=spf1/i.test(r.value));
  const spfThere = type === "TXT" && /^v=spf1/i.test(v) && !!spfAt;
  const problem =
    ["A", "AAAA", "CNAME"].includes(type) && atName.some((r) => r.managed === "tiffin")
        ? `${full} points at your box, and Tiffin manages its address records. Use another name.`
        : type === "CNAME" && atName.length > 0
          ? `${full} already has other records, and a CNAME can’t share its name.`
          : type !== "CNAME" && atName.some((r) => r.type === "CNAME")
            ? `${full} is a CNAME, which can’t share its name with other records.`
            : group.some((r) => r.value === v)
              ? "That record is already there."
              : spfThere
                ? "There’s already an SPF record here. Edit that one instead: a name can only have one."
                : null;

  const unchanged = !!editing && editing.name === full && editing.type === type && editing.value === v;
  const save = useMutation({
    mutationFn: async () => {
      // A name and type hold all their values together: send the ones to keep, plus this one.
      await dnsApi.setRecords([...group.map((r) => ({ type, name: full, value: r.value, ttl: r.ttl })), { type, name: full, value: v }]);
      if (editing && (editing.name !== full || editing.type !== type)) await dnsApi.deleteRecords([{ type: editing.type, name: editing.name, value: editing.value }]);
    },
    onSuccess: () => {
      toast({ title: editing ? `Saved the ${type} record for ${full}.` : `Added a ${type} record for ${full}.`, detail: "Public DNS usually shows it within a minute." });
      onDone();
      close();
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (v && full && !problem && !unchanged && !save.isPending) save.mutate();
  };
  const field = "h-9 w-full rounded-[7px] border border-rule-2 bg-paper-raised px-3 text-[0.8125rem] text-ink outline-hidden placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]";

  return (
    <form onSubmit={submit} className="contents">
      <DialogHeader>
        <DialogTitle>{editing ? `Edit ${editing.type} record` : "Add a DNS record"}</DialogTitle>
        <DialogDescription>
          Saved straight to {zone}’s DNS host. {editing ? "Other records stay as they are." : "Pick a template or fill it in yourself."}
        </DialogDescription>
      </DialogHeader>
      <DialogBody className="space-y-4">
        {!editing && (
          <ToggleGroup.Root
            type="single"
            value={kind}
            onValueChange={(k) => k && pick(k as Kind)}
            aria-label="Template"
            className="flex flex-wrap gap-1.5"
          >
            {(Object.keys(PRESETS) as Kind[]).map((k) => (
              <ToggleGroup.Item
                key={k}
                value={k}
                className={cn(
                  "h-7 rounded-full border px-3 text-[0.8125rem] transition-colors",
                  kind === k ? "border-rule-3 bg-paper-select font-[550] text-ink" : "border-rule-2 text-ink-2 hover:border-rule-3 hover:text-ink",
                )}
              >
                {PRESETS[k].label}
              </ToggleGroup.Item>
            ))}
          </ToggleGroup.Root>
        )}
        {preset.hint && !editing && <p className="-mt-1 text-[0.8125rem] text-ink-3">{preset.hint}</p>}
        {kind === "spf" && !editing && spfAt && (
          <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 rounded-[8px] bg-paper-sunk px-3.5 py-2.5 text-[0.8125rem]">
            <span className="min-w-0 text-ink-2">
              {full} already has one: <code className="ident text-[0.75rem] break-all text-ink">{spfAt.value}</code>
            </span>
            <Button type="button" size="sm" onClick={() => onEdit(spfAt)}>
              Edit that one
            </Button>
          </div>
        )}

        <div className="grid gap-3 sm:grid-cols-[7rem_minmax(0,1fr)]">
          <label className="block">
            <span className="mb-1 block text-xs font-[550] text-ink-2">Type</span>
            <Select value={type} onValueChange={setType} options={TYPES.map((t) => ({ value: t, label: t }))} aria-label="Type" />
          </label>
          <label className="block min-w-0">
            <span className="mb-1 block text-xs font-[550] text-ink-2">Name</span>
            <span className="flex h-9 items-center rounded-[7px] border border-rule-2 bg-paper-raised focus-within:border-brass focus-within:shadow-[0_0_0_3px_var(--brass-wash)]">
              <input
                ref={nameRef}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={preset.namePh ?? "@"}
                spellCheck={false}
                autoCapitalize="off"
                className="ident h-full min-w-0 flex-1 bg-transparent pl-3 text-[0.8125rem] text-ink outline-hidden placeholder:text-ink-4 focus-visible:outline-hidden"
              />
              <span className="ident max-w-[55%] shrink-0 truncate pr-3 text-[0.75rem] text-ink-3">{name.trim() === "" || name.trim() === "@" ? `= ${base}` : full === name.trim().toLowerCase() ? "" : `.${base}`}</span>
            </span>
          </label>
        </div>
        <label className="block">
          <span className="mb-1 block text-xs font-[550] text-ink-2">Value</span>
          {type === "TXT" ? (
            <textarea
              ref={valueRef}
              value={value}
              onChange={(e) => setValue(e.target.value)}
              placeholder={preset.valuePh}
              rows={2}
              spellCheck={false}
              autoFocus={!!editing}
              className={cn(field, "ident block h-auto min-h-[4.25rem] resize-y py-2 leading-5")}
            />
          ) : (
            <input ref={valueRef} value={value} onChange={(e) => setValue(e.target.value)} placeholder={preset.valuePh} spellCheck={false} className={cn(field, "ident")} />
          )}
          <span className="mt-1 block text-xs text-ink-3">{VALUE_HINT[type]}</span>
        </label>
        {v && problem && <p className="text-[0.8125rem] text-danger">{problem}</p>}
        {save.isError && <ProblemNote error={save.error} />}
      </DialogBody>
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={close}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={!v || !full || !!problem || unchanged || save.isPending}>
          {save.isPending ? "Saving…" : editing ? "Save record" : "Add record"}
        </Button>
      </DialogFooter>
    </form>
  );
}
