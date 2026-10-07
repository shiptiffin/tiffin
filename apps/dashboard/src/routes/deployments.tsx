import { useTitle } from "@/components/favicon";
import { Crumbs, Page, PageHeader } from "@/components/page";

/** Deployments for one project. (Being built: see the project sidebar.) */
export function DeploymentsPage({ project }: { project: string }) {
  useTitle("Deployments");
  return (
    <Page wide>
      <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: "Deployments" }]} />} title="Deployments" />
    </Page>
  );
}
