import { PhasePlaceholder } from "@/components/shared/PhasePlaceholder";

export function generateStaticParams() {
  return [{ project: "_" }];
}

export default function ProjectActivityPage() {
  return <PhasePlaceholder title="Activity feed" phase="C" />;
}
