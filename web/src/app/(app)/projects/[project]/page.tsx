import { ProjectDetailView } from "./view";

// Static export needs a known set of params at build time. Emit a single
// placeholder; the Go SPA fallback hands any unknown /projects/<name>
// to the root index, and the client router resolves it at runtime.
export function generateStaticParams() {
  return [{ project: "_" }];
}

export default function ProjectDetailPage() {
  return <ProjectDetailView />;
}
