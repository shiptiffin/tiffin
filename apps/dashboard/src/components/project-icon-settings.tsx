import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ImageUp } from "lucide-react";
import { ToggleGroup } from "radix-ui";
import { useRef, useState, type DragEvent, type ReactNode } from "react";
import { api } from "@/api/client";
import { ICON_MAX_BYTES, iconQuery, resetIcon, uploadIcon, type IconInfo } from "@/api/modules";
import { IconTile, Monogram, letters } from "@/components/project-icon";
import { ProblemNote } from "@/components/problem";
import { Working } from "@/components/project-rows";
import { Skeleton } from "@/components/page";
import { toast } from "@/components/toast";
import { cn } from "@/lib/cn";
import { ENAMELS, appearanceQuery, enamelNames, enamelVar, useEnamel, type Enamel } from "@/lib/enamel";
import { useMe } from "@/lib/me";
import { relative } from "@/lib/time";

const TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml", "image/x-icon", "image/vnd.microsoft.icon"];
const ACCEPT = ".png,.jpg,.jpeg,.gif,.webp,.svg,.ico," + TYPES.join(",");

type Choice = "app" | "upload" | "letter";

/**
 * Settings › General › Icon: three choices, each drawn as it would look.
 * The app's own icon (found after each production deploy), an uploaded
 * image (click or drop a file), or the project's initials on its colour,
 * with the six colours to pick from. Read-only people see what's chosen.
 */
export function ProjectIconSettings({ project }: { project: string }) {
  const qc = useQueryClient();
  const info = useQuery(iconQuery(project));
  const enamel = useEnamel(project);
  const { can } = useMe();
  const canEdit = can("apply:reversible");
  const file = useRef<HTMLInputElement>(null);
  const [local, setLocal] = useState<string>();

  const done = (r: IconInfo, title: string) => {
    qc.setQueryData(iconQuery(project).queryKey, r);
    toast({ title });
  };
  const upload = useMutation({
    mutationFn: async (f: File) => {
      const why = refuse(f);
      if (why) throw new Error(why);
      const png = f.type === "image/svg+xml" || f.name.toLowerCase().endsWith(".svg") ? await rasterize(f) : undefined;
      return uploadIcon(project, f, png);
    },
    onSuccess: (r) => done(r, `${project} has a new icon.`),
    onSettled: () => setLocal(undefined),
  });
  const reset = useMutation({
    mutationFn: (use: "app" | "letter") => resetIcon(project, use),
    onSuccess: (r, use) =>
      done(r, use === "letter" ? `${project} shows its letters.` : r.showing === "inferred" ? `${project} shows its app’s icon.` : `${project} will show its app’s icon once the box finds one.`),
  });
  const colour = useMutation({
    mutationFn: (e: Enamel) => api.setAppearance(project, e),
    onSuccess: (r) => {
      qc.setQueryData(appearanceQuery(project).queryKey, r);
      void qc.invalidateQueries({ queryKey: ["project-icon", project] });
    },
  });

  const pick = (f?: File) => {
    if (!f || !canEdit) return;
    upload.reset();
    reset.reset();
    setLocal(f.name);
    upload.mutate(f);
  };
  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    pick(e.dataTransfer.files[0]);
  };

  if (info.isPending) return <Skeleton className="h-[4.5rem] max-w-[40rem]" />;
  const d = info.data;
  const current: Choice = d?.mode === "upload" ? "upload" : d?.mode === "letter" ? "letter" : "app";
  const busy = upload.isPending ? "upload" : reset.isPending ? reset.variables : undefined;
  const value = busy === "letter" ? "letter" : busy === "app" ? "app" : current;
  const found = d?.found;
  const check = d?.lastCheck;
  const mark = letters(project);

  return (
    <div className="max-w-[44rem]">
      {/* A single-choice toggle group (role radiogroup): arrow keys only move
          focus, since every pick is saved at once; Space or Enter picks. */}
      <ToggleGroup.Root
        type="single"
        value={value}
        disabled={!canEdit}
        aria-label={`${project}’s icon`}
        onValueChange={(v) => {
          if (v !== "app" && v !== "letter") return; // "" (the chosen one again) or upload, which opens the file picker
          upload.reset();
          reset.mutate(v);
        }}
        className="grid gap-2 sm:grid-cols-3"
      >
        <Option
          value="app"
          tile={found ? <IconTile image={found} size={32} /> : <Empty size={32} />}
          title="App’s favicon"
          sub={
            busy === "app" ? (
              <Working>Looking…</Working>
            ) : found ? (
              <span className="ident text-[0.75rem]">{found.source === "inline" ? "inline in the page" : found.source}</span>
            ) : (
              "None found yet"
            )
          }
        />
        <Option
          value="upload"
          onClick={() => file.current?.click()}
          onDrop={canEdit ? onDrop : undefined}
          tile={d?.mode === "upload" && d.image ? <IconTile image={d.image} size={32} /> : <UploadTile />}
          title={d?.mode === "upload" ? "Uploaded" : "Upload"}
          sub={busy === "upload" ? <Working>Uploading {local}…</Working> : d?.mode === "upload" && d.image ? `${kind(d.image.mime)} · click to replace` : "PNG, SVG, ICO · 512 KB"}
        />
        <Option value="letter" tile={<Monogram letters={mark} enamel={enamel} size={32} />} title="Letters" sub={<>{mark} on {enamelNames[enamel].toLowerCase()}</>} />
      </ToggleGroup.Root>
      <input ref={file} type="file" accept={ACCEPT} className="sr-only" tabIndex={-1} aria-hidden onChange={(e) => (pick(e.target.files?.[0]), (e.target.value = ""))} />

      {value === "letter" && (
        <div className="mt-4 flex items-center gap-3">
          <span className="text-[0.8125rem] text-ink-3">Colour</span>
          <ToggleGroup.Root
            type="single"
            value={colour.isPending ? colour.variables : enamel}
            onValueChange={(v) => v && colour.mutate(v as Enamel)}
            disabled={!canEdit}
            aria-label={`${project}’s colour`}
            className="flex items-center gap-1.5"
          >
            {ENAMELS.map((e) => (
              <ToggleGroup.Item
                key={e}
                value={e}
                aria-label={enamelNames[e]}
                title={enamelNames[e]}
                className="group grid size-6 place-items-center rounded-full outline-offset-2 transition-transform duration-[var(--dur-state)] hover:scale-110 data-[disabled]:hover:scale-100"
                style={{ background: enamelVar(e) }}
              >
                <Check className="hidden size-3.5 text-paper-raised group-data-[state=on]:block" strokeWidth={3} aria-hidden />
              </ToggleGroup.Item>
            ))}
          </ToggleGroup.Root>
        </div>
      )}

      {explain(d, value) && <p className="mt-3 text-[0.8125rem] text-ink-3">{explain(d, value)}</p>}
      {check && !check.found && check.reason && value === "app" && !found && (
        <p className="mt-1 text-[0.8125rem] text-ink-3">
          Last looked {relative(check.at)} on {check.app}: {check.reason}.
        </p>
      )}
      {!canEdit && <p className="mt-1 text-[0.8125rem] text-ink-3">You can see the icon but not change it; ask an admin of this box.</p>}
      {(upload.error || reset.error || colour.error) && <ProblemNote className="mt-3" error={upload.error ?? reset.error ?? colour.error} />}
    </div>
  );
}

function Option({ value, tile, title, sub, onClick, onDrop }: { value: Choice; tile: ReactNode; title: string; sub: ReactNode; onClick?: () => void; onDrop?: (e: DragEvent) => void }) {
  const [over, setOver] = useState(false);
  return (
    <ToggleGroup.Item
      value={value}
      onClick={onClick}
      onDragOver={onDrop ? (e) => (e.preventDefault(), setOver(true)) : undefined}
      onDragLeave={onDrop ? () => setOver(false) : undefined}
      onDrop={onDrop ? (e) => (setOver(false), onDrop(e)) : undefined}
      className={cn(
        "group flex min-w-0 items-center gap-3 rounded-[10px] border px-3 py-2.5 text-left transition-colors duration-[var(--dur-state)]",
        "data-[state=on]:border-brass data-[state=on]:bg-brass-wash data-[disabled]:cursor-default",
        over ? "border-brass border-dashed" : "border-rule-2 hover:border-rule-3 data-[disabled]:hover:border-rule-2",
      )}
    >
      {tile}
      <span className="min-w-0">
        <span className="block text-[0.875rem] font-[550] text-ink">{title}</span>
        <span className="block truncate text-[0.75rem] text-ink-3">{sub}</span>
      </span>
    </ToggleGroup.Item>
  );
}

function Empty({ size }: { size: number }) {
  return <span className="inline-block shrink-0 rounded-[8px] border border-dashed border-rule-3" style={{ width: size, height: size }} aria-hidden />;
}

function UploadTile() {
  return (
    <span className="grid size-8 shrink-0 place-items-center rounded-[8px] border border-dashed border-rule-3 text-ink-3 group-hover:text-ink-2" aria-hidden>
      <ImageUp className="size-4" />
    </span>
  );
}

const kind = (mime: string) => (mime === "image/svg+xml" ? "SVG" : "PNG");

function explain(d: IconInfo | undefined, value: Choice) {
  if (value === "upload") return "Square works best, 64 px or larger. Drop another file on Upload to replace it.";
  if (value === "letter") return undefined;
  if (d?.showing === "inferred") return "The box checks your app for a new icon after each production deploy.";
  return "After each production deploy the box looks for your app’s icon (its favicon, Apple touch icon or web manifest). Until it finds one, the project shows its letters.";
}

/** Says why a file can't be an icon, before it's sent. */
function refuse(f: File) {
  const ok = TYPES.includes(f.type) || /\.(png|jpe?g|gif|webp|svg|ico)$/i.test(f.name);
  if (!ok) return `${f.name} isn’t an image the box takes: use a PNG, JPEG, GIF, WebP, ICO or SVG.`;
  if (f.size > ICON_MAX_BYTES) return `${f.name} is ${Math.round(f.size / 1024)} KB; icons can be up to 512 KB. Export it smaller (256 px is plenty).`;
  return undefined;
}

/** A 256 px PNG of an SVG, for email (which can't show SVG). Undefined when the browser can't draw it. */
async function rasterize(svg: File): Promise<Blob | undefined> {
  try {
    const url = await new Promise<string>((ok, bad) => {
      const r = new FileReader();
      r.onload = () => ok(String(r.result));
      r.onerror = () => bad(r.error);
      r.readAsDataURL(svg.type ? svg : new File([svg], svg.name, { type: "image/svg+xml" }));
    });
    const img = new Image();
    img.src = url;
    await img.decode();
    const side = 256;
    const c = document.createElement("canvas");
    c.width = c.height = side;
    const ctx = c.getContext("2d");
    if (!ctx) return undefined;
    const w = img.naturalWidth || side;
    const h = img.naturalHeight || side;
    const s = side / Math.max(w, h);
    ctx.drawImage(img, (side - w * s) / 2, (side - h * s) / 2, w * s, h * s);
    return await new Promise<Blob | undefined>((ok) => c.toBlob((b) => ok(b ?? undefined), "image/png"));
  } catch {
    return undefined;
  }
}
