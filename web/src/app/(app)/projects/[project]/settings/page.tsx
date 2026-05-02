import { PhasePlaceholder } from "@/components/shared/PhasePlaceholder";

export function generateStaticParams() {
  return [{ project: "_" }];
}

export default function ProjectSettingsPage() {
  return <PhasePlaceholder title="Project settings" phase="C" />;
}
