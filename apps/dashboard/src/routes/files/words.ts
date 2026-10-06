// Small words and rules the Files console shares.

export type Kind = "image" | "video" | "audio" | "pdf" | "text" | "file";

const ext = (key: string) => key.slice(key.lastIndexOf(".") + 1).toLowerCase();

const KINDS: Record<string, Kind> = {
  png: "image",
  jpg: "image",
  jpeg: "image",
  gif: "image",
  webp: "image",
  avif: "image",
  svg: "image",
  ico: "image",
  bmp: "image",
  mp4: "video",
  webm: "video",
  mov: "video",
  m4v: "video",
  ogv: "video",
  mp3: "audio",
  wav: "audio",
  ogg: "audio",
  m4a: "audio",
  flac: "audio",
  aac: "audio",
  opus: "audio",
  pdf: "pdf",
  txt: "text",
  md: "text",
  json: "text",
  csv: "text",
  tsv: "text",
  log: "text",
  xml: "text",
  yml: "text",
  yaml: "text",
  toml: "text",
  ini: "text",
  html: "text",
  htm: "text",
  css: "text",
  js: "text",
  mjs: "text",
  ts: "text",
  tsx: "text",
  jsx: "text",
  sql: "text",
  sh: "text",
  env: "text",
};

/** What a file is, by its name: decides the preview and the icon. */
export function kindOf(key: string): Kind {
  return KINDS[ext(key)] ?? (/(^|\/)(README|LICENSE|Dockerfile)$/.test(key) ? "text" : "file");
}

/** Images the box can resize (libvips reads these; SVG and icons stay as they are). */
export function resizable(key: string): boolean {
  return ["png", "jpg", "jpeg", "gif", "webp", "avif"].includes(ext(key));
}

/** The last part of a key or folder: "covers/2026/" → "2026", "a/b.jpg" → "b.jpg". */
export function baseName(key: string): string {
  const k = key.endsWith("/") ? key.slice(0, -1) : key;
  return k.slice(k.lastIndexOf("/") + 1);
}

/** A guess at a file's type from its name, for uploads the browser gives no type. */
const MIME: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  avif: "image/avif",
  svg: "image/svg+xml",
  mp4: "video/mp4",
  webm: "video/webm",
  mov: "video/quicktime",
  mp3: "audio/mpeg",
  wav: "audio/wav",
  ogg: "audio/ogg",
  m4a: "audio/mp4",
  pdf: "application/pdf",
  txt: "text/plain",
  md: "text/markdown",
  json: "application/json",
  csv: "text/csv",
  html: "text/html",
  css: "text/css",
  js: "text/javascript",
  zip: "application/zip",
};
export function mimeOf(name: string, given?: string): string {
  return given || MIME[ext(name)] || "application/octet-stream";
}

/** Whether a type passes a bucket's allowed types ("image/*" matches a family). */
export function typeAllowed(allowed: string[] | null | undefined, type: string): boolean {
  if (!allowed?.length) return true;
  const t = type.toLowerCase().split(";")[0].trim();
  return allowed.some((g) => {
    const x = g.toLowerCase();
    return x === t || (x.endsWith("/*") && t.startsWith(x.slice(0, -1)));
  });
}

/** The type families people pick from, as the bucket's allowedTypes. */
export const TYPE_FAMILIES: Array<{ label: string; types: string[] }> = [
  { label: "Images", types: ["image/*"] },
  { label: "Video", types: ["video/*"] },
  { label: "Audio", types: ["audio/*"] },
  { label: "PDFs", types: ["application/pdf"] },
  { label: "Text", types: ["text/*", "application/json"] },
];

/** "Images and PDFs", "Any type": a bucket's allowed types in words. */
export function typesWords(allowed: string[] | null | undefined): string {
  if (!allowed?.length) return "Any type";
  const named = TYPE_FAMILIES.filter((f) => f.types.every((t) => allowed.includes(t)));
  const rest = allowed.filter((t) => !named.some((f) => f.types.includes(t)));
  const parts = [...named.map((f) => f.label), ...rest];
  return parts.length > 1 ? `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}` : parts[0];
}

/** Sizes people pick from for the largest file. */
export const SIZE_STOPS = [0, 1, 5, 10, 25, 50, 100, 250, 500, 1024, 5120].map((mb) => mb * 1024 * 1024);

/** The widths the box resizes to (Next.js's image sizes), and the qualities. */
export const WIDTHS = [16, 32, 48, 64, 96, 128, 256, 384, 640, 750, 828, 1080, 1200, 1920, 2048, 3840];
export const QUALITIES = [50, 75, 90, 100];

/** How long a private link works. */
export const EXPIRIES: Array<{ label: string; seconds: number }> = [
  { label: "1 hour", seconds: 3600 },
  { label: "1 day", seconds: 86_400 },
  { label: "7 days", seconds: 604_800 },
];

/** A natural sort: "img2" before "img10". */
export const byName = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" });

/** Valid S3 bucket names here: lowercase letters, digits and dashes, starting with a letter. */
export const BUCKET_NAME = /^[a-z][a-z0-9-]{0,39}$/;

/** Typing in a field shouldn't trigger page shortcuts. */
export function typing(e: KeyboardEvent): boolean {
  const t = e.target;
  return e.metaKey || e.ctrlKey || e.altKey || (t instanceof Element && !!t.closest("input, textarea, select, [contenteditable], [role=dialog]"));
}
