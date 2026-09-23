import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider, type QueryKey } from "@tanstack/react-query";
import type { ReactElement } from "react";
import { errorsQueryKey } from "@/features/services";
import { ServiceErrorsPanel } from "@/components/service/overlay/ServiceErrorsPanel";
import UpdatesPage from "@/app/(app)/settings/updates/page";

// Server-render against a cache whose query has already failed. SSR
// never runs the queryFn, so what renders is exactly the error branch.
function renderWithFailedQuery(key: QueryKey, ui: ReactElement): string {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, retryOnMount: false } } });
  qc.getQueryCache()
    .build(qc, { queryKey: key })
    .setState({ status: "error", error: new Error("upstream 502"), fetchStatus: "idle" });
  return renderToStaticMarkup(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe("failed queries render an error, not an empty state", () => {
  it("Errors tab says it couldn't load instead of 'No errors detected'", () => {
    const html = renderWithFailedQuery(
      errorsQueryKey("alpha", "web", "24h"),
      <ServiceErrorsPanel project="alpha" service="web" />,
    );
    expect(html).not.toContain("No errors detected");
    expect(html).toContain("upstream 502");
  });

  it("Settings → Updates renders an error when the version call fails", () => {
    const html = renderWithFailedQuery(["system", "version"], <UpdatesPage />);
    expect(html).toContain("upstream 502");
  });
});
