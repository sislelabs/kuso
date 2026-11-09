import { Suspense } from "react";
import { ServicePageView } from "./view";

// Static export: one placeholder; the Go SPA fallback maps every
// /projects/<p>/services/<s> to projects/_/services/_.html.
export function generateStaticParams() {
  return [{ project: "_", service: "_" }];
}

export default function ServicePage() {
  // ServicePageView reads ?tab=/?env= with useSearchParams.
  return (
    <Suspense fallback={null}>
      <ServicePageView />
    </Suspense>
  );
}
