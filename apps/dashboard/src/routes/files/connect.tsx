import { useQuery } from "@tanstack/react-query";
import { Eye } from "lucide-react";
import { useState } from "react";
import { mod, type StorageBucket } from "@/api/modules";
import { CopyButton } from "@/components/copy";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useMe } from "@/lib/me";
import { Segmented } from "@/routes/kv/parts";

type Lang = "bun" | "aws" | "sdk";

/**
 * How apps reach the buckets: the env every app in the project already
 * has, by name, and a few lines to use it. The secret key only for people
 * with full access, on request (recorded).
 */
export function FilesConnect({
  project,
  buckets,
  open,
  onOpenChange,
}: {
  project: string;
  buckets: StorageBucket[];
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  const { can } = useMe();
  const [reveal, setReveal] = useState(false);
  const [lang, setLang] = useState<Lang>("bun");
  const c = useQuery({
    queryKey: ["files-connection", project, reveal],
    queryFn: () => mod.filesConnection(project, reveal),
    enabled: open,
    staleTime: 60_000,
  });
  const b = buckets.find((x) => !x.public) ?? buckets[0];
  const name = b?.name ?? "uploads";
  const env = `S3_BUCKET_${name.toUpperCase().replace(/-/g, "_")}`;
  const snippets: Record<Lang, string> = {
    bun: `import { s3 } from "bun";

// Bun.s3 reads S3_* from the env the box sets.
const file = s3.file("avatars/ada.png", { bucket: process.env.${env} });
await file.write(photo, { type: "image/png" });
const link = file.presign({ expiresIn: 600 }); // 10 minutes`,
    aws: `import { S3Client, PutObjectCommand } from "@aws-sdk/client-s3";

// The AWS SDK reads AWS_* from the env the box sets.
const s3 = new S3Client({ forcePathStyle: true });
await s3.send(new PutObjectCommand({ Bucket: process.env.${env}, Key: "avatars/ada.png", Body: photo }));`,
    sdk: `import { upload, ${b?.public ? "publicUrl" : "signedUrl"} } from "tiffin-sdk/storage";

await upload("${name}", "avatars/ada.png", photo, { contentType: "image/png" });
const src = ${b?.public ? "publicUrl" : "signedUrl"}("${name}", "avatars/ada.png", { width: 256 }); // resized`,
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Connect to the files</DialogTitle>
          <DialogDescription>
            Every app in {project} already has these, so there is nothing to set. Any S3 client works: Bun.s3, the AWS SDKs, aws-cli.
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          {c.isError && <ProblemNote error={c.error} />}
          {c.data && (
            <div className="max-h-64 overflow-y-auto rounded-[10px] border border-rule-2">
              {(c.data.env ?? []).map((e) => (
                <div
                  key={e.name}
                  className="grid grid-cols-[minmax(0,14rem)_minmax(0,1fr)_auto] items-center gap-3 border-b border-rule px-3 py-1.5 last:border-b-0"
                >
                  <code className="truncate font-mono text-[0.78125rem] text-ink">{e.name}</code>
                  <code
                    className={e.value ? "truncate font-mono text-[0.78125rem] text-ink-2" : "font-sans text-sm text-ink-3"}
                    title={e.value || undefined}
                  >
                    {e.value || (e.secret ? "hidden" : "")}
                  </code>
                  {e.value ? <CopyButton value={e.value} label={`Copy ${e.name}`} className="size-6" /> : <span className="size-6" />}
                </div>
              ))}
            </div>
          )}
          {c.data && !c.data.revealed && (
            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-sm text-ink-3">
                {can("apply:irreversible")
                  ? "The secret key can read and delete every file, so it stays hidden until you ask. Showing it is recorded."
                  : "Showing the secret key needs full access to this project."}
              </p>
              {can("apply:irreversible") && (
                <Button size="sm" onClick={() => setReveal(true)}>
                  <Eye />
                  Show the key
                </Button>
              )}
            </div>
          )}
          <div>
            <Segmented
              label="Code for"
              value={lang}
              onChange={setLang}
              options={[
                { value: "bun", label: "Bun" },
                { value: "aws", label: "AWS SDK" },
                { value: "sdk", label: "tiffin-sdk" },
              ]}
            />
            <div className="mt-2 overflow-hidden rounded-[10px] border border-rule-2 bg-paper-sunk">
              <div className="flex items-center justify-between border-b border-rule py-1 pr-1.5 pl-3.5">
                <span className="ident text-ink-3">upload.ts</span>
                <CopyButton value={snippets[lang]} label="Copy code" />
              </div>
              <pre className="overflow-x-auto px-3.5 py-3 font-mono text-[0.75rem] leading-5 text-ink-2">
                <code>{snippets[lang]}</code>
              </pre>
            </div>
          </div>
          {c.data && (
            <p className="text-sm text-ink-3">
              From your computer: the endpoint is <code className="font-mono text-ink-2">{c.data.endpoint.replace(/^https?:\/\//, "")}</code>, region{" "}
              <code className="font-mono text-ink-2">{c.data.region}</code>, path-style addressing.
            </p>
          )}
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}
