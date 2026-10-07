import { useTitle } from "@/components/favicon";
import { Crumbs, Page, PageHeader } from "@/components/page";

/** Observability for one project. (Being built: see the project sidebar.) */
export function ObservabilityPage({ project }: { project: string }) {
  useTitle("Observability");
  return (
    <Page wide>
      <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: "Observability" }]} />} title="Observability" />
    </Page>
  );
}
