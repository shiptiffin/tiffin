import { useMutation } from "@tanstack/react-query";
import { Eye, EyeOff } from "lucide-react";
import { useState, type ReactNode } from "react";
import { mod, type StorageInfo } from "@/api/modules";
import { CopyButton } from "@/components/copy";
import { Section } from "@/components/data-parts";
import { InfoTip } from "@/components/info-tip";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/clipboard";
import { useMe } from "@/lib/me";

const envName = (b: string) => b.toUpperCase().replace(/-/g, "_");

/**
 * S3 access for tools and apps off the box: the public endpoint, region and
 * key, each with Copy. The secret shows only on Reveal (the box records each
 * time), and Copy as .env hands over the lot with the public address.
 */
export function FilesAccess({ project, s }: { project: string; s: StorageInfo }) {
  const { can } = useMe();
  const full = can("apply:irreversible");
  const [shown, setShown] = useState(false);
  const reveal = useMutation({ mutationFn: () => mod.credentials(project), onSuccess: () => setShown(true) });
  const env = reveal.data;
  const secret = env?.S3_SECRET_ACCESS_KEY ?? env?.AWS_SECRET_ACCESS_KEY;
  const keyId = s.accessKeyId ?? env?.S3_ACCESS_KEY_ID;
  const buckets = s.buckets ?? [];

  const dotenv = () =>
    [
      `S3_ENDPOINT=${s.endpoint}`,
      `S3_REGION=${s.region}`,
      `S3_ACCESS_KEY_ID=${keyId ?? ""}`,
      `S3_SECRET_ACCESS_KEY=${secret ?? ""}`,
      "S3_FORCE_PATH_STYLE=true",
      ...buckets.map((b) => `S3_BUCKET_${envName(b.name)}=${b.s3Name}`),
    ].join("\n") + "\n";

  return (
    <Section
      className="mt-12"
      id="s3"
      label="S3 access"
      aside={
        <span className="inline-flex items-center gap-1">
          For tools and apps off the box
          <InfoTip label="About S3 access">
            Apps on the box already get these as S3_* and AWS_* env vars, pointed at the box’s inside address. Use these for the AWS CLI, Cyberduck, rclone
            or an app running elsewhere.
          </InfoTip>
        </span>
      }
    >
      <dl className="divide-y divide-rule border-y border-rule">
        <Row label="Endpoint" value={s.endpoint} />
        <Row label="Region" value={s.region} />
        {keyId && <Row label="Access key ID" value={keyId} />}
        <Row
          label="Secret access key"
          value={shown && secret ? secret : undefined}
          display={
            shown && secret ? undefined : (
              <span className="font-mono text-[0.8125rem] tracking-[0.12em] text-ink-3">
                <span aria-hidden>••••••••••••••••••••</span>
                <span className="sr-only">Hidden</span>
              </span>
            )
          }
          action={
            full ? (
              <Button
                size="sm"
                variant="ghost"
                disabled={reveal.isPending}
                onClick={() => (shown ? setShown(false) : env ? setShown(true) : reveal.mutate())}
              >
                {shown ? <EyeOff /> : <Eye />}
                {shown ? "Hide" : reveal.isPending ? "Revealing…" : "Reveal"}
              </Button>
            ) : (
              <span className="text-xs text-ink-3">Needs full access</span>
            )
          }
        />
        <Row label="Addressing" display={<span className="text-sm text-ink-2">Path-style: the bucket goes in the path, not the host name.</span>} />
        {buckets.length > 0 && (
          <Row
            label="Bucket names"
            display={
              <ul className="space-y-1 py-0.5">
                {buckets.map((b) => (
                  <li key={b.name} className="flex min-w-0 items-center gap-2 text-sm">
                    <code className="ident truncate text-ink">{b.s3Name}</code>
                    <CopyButton value={b.s3Name} label={`Copy ${b.s3Name}`} className="size-6" />
                    <span className="truncate font-mono text-[0.75rem] text-ink-3 max-sm:hidden">S3_BUCKET_{envName(b.name)}</span>
                  </li>
                ))}
              </ul>
            }
          />
        )}
      </dl>
      {reveal.isError && <ProblemNote className="mt-3" error={reveal.error} />}
      <div className="mt-3 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <p className="text-xs text-ink-3">
          {shown ? "Keep the secret out of shared screens, chats and committed files. Each reveal is recorded." : "The key can read and delete every file in these buckets."}
        </p>
        {full && (
          <Button
            size="sm"
            onClick={async () => {
              let ok = !!secret;
              if (!ok) ok = !!(await reveal.mutateAsync().catch(() => null));
              if (ok && (await copyText(dotenv()))) toast({ title: "Copied the S3 settings as .env.", detail: "With the box’s public address, for use off the box." });
            }}
          >
            Copy as .env
          </Button>
        )}
      </div>
    </Section>
  );
}

function Row({ label, value, display, action }: { label: string; value?: string; display?: ReactNode; action?: ReactNode }) {
  return (
    <div className="grid items-center gap-x-4 py-2 max-sm:pt-2.5 sm:grid-cols-[11rem_minmax(0,1fr)]">
      <dt className="text-sm text-ink-3">{label}</dt>
      <dd className="flex min-h-8 min-w-0 items-center gap-1">
        <span className="min-w-0 flex-1">
          {display ?? (
            <code className="ident block truncate text-[0.8125rem] text-ink" title={value}>
              {value}
            </code>
          )}
        </span>
        {value && <CopyButton value={value} label={`Copy the ${label.toLowerCase()}`} className="size-7" />}
        {action}
      </dd>
    </div>
  );
}
