import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { getInsights, type InsightsRange } from "./api";

// The server caches the Prometheus side for a minute, so polling faster
// buys nothing.
export function useInsights(range: InsightsRange) {
  return useQuery({
    queryKey: ["insights", range],
    queryFn: () => getInsights(range),
    refetchInterval: 60_000,
    refetchIntervalInBackground: false,
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  });
}
