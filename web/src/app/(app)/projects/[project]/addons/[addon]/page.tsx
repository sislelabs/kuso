import { Suspense } from "react";
import { AddonPageView } from "./view";

// Static export: one placeholder; the Go SPA fallback maps every
// /projects/<p>/addons/<a> to projects/_/addons/_.html.
export function generateStaticParams() {
  return [{ project: "_", addon: "_" }];
}

export default function AddonPage() {
  return (
    <Suspense fallback={null}>
      <AddonPageView />
    </Suspense>
  );
}
