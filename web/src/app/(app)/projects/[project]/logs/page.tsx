import { LogsView } from "./view";

export function generateStaticParams() {
  return [{ project: "_" }];
}

export default function ProjectLogsPage() {
  return <LogsView />;
}
