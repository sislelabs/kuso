import { ProjectSettingsView } from "./view";

export function generateStaticParams() {
  return [{ project: "_" }];
}

export default function ProjectSettingsPage() {
  return <ProjectSettingsView />;
}
