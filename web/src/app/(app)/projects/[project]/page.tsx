import { PhasePlaceholder } from "@/components/shared/PhasePlaceholder";

// Static export of dynamic segments: emit a single placeholder HTML the
// SPA router resolves at runtime. The Go server serves index.html for
// any unknown sub-path under /projects/, so this generated page is a
// fallback shell.
export function generateStaticParams() {
  return [{ project: "_" }];
}

export default function ProjectCanvasPage() {
  return (
    <PhasePlaceholder
      title="Project canvas"
      phase="G"
      description="The React Flow canvas with services + addons + animated connections lands in Phase G."
    />
  );
}
