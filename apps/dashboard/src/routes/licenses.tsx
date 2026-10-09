import { useQuery } from "@tanstack/react-query";
import { useTitle } from "@/components/favicon";
import { Group } from "@/components/health-kit";
import { Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { healthQuery, sourceURL } from "@/lib/box";

const licensesText = {
  queryKey: ["licenses-text"],
  queryFn: async () => {
    const r = await fetch("/licenses.txt");
    if (!r.ok) throw new Error(`The box answered ${r.status}.`);
    return r.text();
  },
  staleTime: Infinity,
} as const;

/**
 * Licenses: Tiffin is AGPL-3.0-only, with a link to the source code of the
 * version this box runs (AGPL-3.0 section 13); what stays Apache-2.0 inside
 * people's apps; then the third-party notices the binary carries
 * (/licenses.txt, the same text as tiffin licenses).
 */
export function LicensesPage() {
  useTitle("Licenses");
  const health = useQuery(healthQuery);
  const text = useQuery(licensesText);
  const source = sourceURL(health.data);

  return (
    <Page>
      <PageHeader title="Licenses" lede="Tiffin is free software: you may run, study, change and share it." />

      <dl className="mt-8 grid grid-cols-[8rem_minmax(0,1fr)] text-[0.875rem]">
        {(
          [
            ["Tiffin", <>GNU Affero General Public License v3.0 (AGPL-3.0-only)</>],
            [
              "Source code",
              <a className="ident underline underline-offset-2" href={source} target="_blank" rel="noreferrer">
                {source.replace(/^https:\/\/github\.com\//, "").replace("/tree/", " at ")}
              </a>,
            ],
            ["Your apps", <>The SDK, starters, templates, analytics script and build files that go into your apps are Apache-2.0, so your apps stay yours.</>],
          ] as const
        ).map(([k, v]) => (
          <div key={k} className="col-span-2 grid grid-cols-subgrid border-b border-rule py-2.5 first:border-t">
            <dt className="text-ink-3">{k}</dt>
            <dd className="min-w-0 text-ink">{v}</dd>
          </div>
        ))}
      </dl>

      <Group
        label="Third-party software"
        id="third-party"
        aside={
          <a className="underline underline-offset-2" href="/licenses.txt" target="_blank" rel="noreferrer">
            Open as text
          </a>
        }
      >
        {text.isPending && <Skeleton className="h-96" />}
        {text.isError && <ProblemNote error={text.error} title="Couldn’t load the notices." />}
        {text.isSuccess && (
          <pre tabIndex={0} aria-label="Third-party notices" className="ident max-h-[70vh] overflow-auto border-y border-rule py-4 text-[0.75rem] leading-5 whitespace-pre text-ink-2">{text.data}</pre>
        )}
      </Group>
    </Page>
  );
}
