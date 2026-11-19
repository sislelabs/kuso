"use client";

import { Suspense } from "react";
import { InsightsView } from "@/components/insights/InsightsView";

// useSearchParams needs a Suspense boundary in the static export.
export default function InsightsPage() {
  return (
    <Suspense fallback={null}>
      <InsightsView />
    </Suspense>
  );
}
