import { ActivityView } from "./view";

export function generateStaticParams() {
  return [{ project: "_" }];
}

export default function ProjectActivityPage() {
  return <ActivityView />;
}
