import { Suspense } from "react";
import { ProjectDetailView } from "./view";

// Static export needs a known set of params at build time. Emit a single
// placeholder; the Go SPA fallback hands any unknown /projects/<name>
// to the root index, and the client router resolves it at runtime.
export function generateStaticParams() {
  return [{ project: "_" }];
}

export default function ProjectDetailPage() {
  // Suspense boundary required because ProjectDetailView calls
  // useSearchParams. Next.js 15+/16 with `output: "export"` will
  // either bail out the prerender or fail the build without one.
  // The fallback is null because the surrounding shell (TopNav,
  // sidebar) renders synchronously; this is just for the search-
  // params hook itself.
  return (
    <Suspense fallback={null}>
      <ProjectDetailView />
    </Suspense>
  );
}
