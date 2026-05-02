import { PhasePlaceholder } from "@/components/shared/PhasePlaceholder";

export function generateStaticParams() {
  return [{ project: "_" }];
}

export default function ProjectLogsPage() {
  return <PhasePlaceholder title="Live logs" phase="D" />;
}
