import { useMutation } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect } from "react";
import { deploysApi } from "@/api/modules";
import { Page } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";

/**
 * Where a deploy's own address (d-<id>--<app>) sends people who aren't
 * signed in there yet. Signed in here (a 401 goes to the login page and
 * comes back), the box hands out a one-use link that opens that address
 * for an hour; this page follows it at once.
 */
export function GatePage({ host, next }: { host?: string; next?: string }) {
  const link = useMutation({
    mutationFn: () => deploysApi.deployLink(`https://${host}${next ?? "/"}`),
    onSuccess: (l) => location.replace(l.url),
  });
  const { mutate } = link;
  useEffect(() => {
    if (host) mutate();
  }, [host, mutate]);

  return (
    <Page>
      {!host ? (
        <p className="text-md text-ink-2">This page opens an earlier version of an app. Its address says which.</p>
      ) : link.isError ? (
        <>
          <h1 className="sentence text-ink">That version can’t be opened.</h1>
          <ProblemNote className="mt-4 max-w-[40rem]" error={link.error} />
          <p className="mt-4 text-sm text-ink-2">
            <Link to="/" className="font-[550] text-brass-ink underline underline-offset-4">
              Back to your projects
            </Link>
          </p>
        </>
      ) : (
        <p className="flex items-center gap-2.5 text-md text-ink-2" role="status">
          <PilotLight state="busy" label="Opening" />
          Opening <span className="ident truncate text-[0.8125rem] text-ink">{host}</span>…
        </p>
      )}
    </Page>
  );
}
