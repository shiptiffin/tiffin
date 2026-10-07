import { useTitle } from "@/components/favicon";
import { Crumbs, Page, PageHeader } from "@/components/page";

/** Environment Variables for one project. (Being built: see the project sidebar.) */
export function EnvVarsPage({ project }: { project: string }) {
  useTitle("Environment Variables");
  return (
    <Page wide>
      <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: "Environment Variables" }]} />} title="Environment Variables" />
    </Page>
  );
}
