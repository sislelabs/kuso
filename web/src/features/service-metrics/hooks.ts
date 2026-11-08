"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { getEnvPodMetrics, getEnvTimeseries, getServiceUsageHistory } from "./api";
import { getSamples, recordSample } from "./history";
import type { ResourceSample } from "./history";

export function useEnvPodMetrics(env: string) {
  const q = useQuery({
    queryKey: ["kubernetes", "envs", env, "metrics"],
    queryFn: () => getEnvPodMetrics(env),
    enabled: !!env,
    refetchInterval: 15_000,
    staleTime: 10_000,
  });
  const [samples, setSamples] = useState<ResourceSample[]>(() => getSamples(env));
  useEffect(() => setSamples(getSamples(env)), [env]);
  useEffect(() => {
    if (q.data && env) setSamples(recordSample(env, q.data.pods ?? [], Date.now()));
  }, [q.data, env]);
  return { ...q, samples };
}

export function useEnvTimeseries(env: string, range: string) {
  return useQuery({
    queryKey: ["kubernetes", "envs", env, "timeseries", range],
    queryFn: () => getEnvTimeseries(env, range),
    enabled: !!env,
    refetchInterval: 30_000,
    staleTime: 20_000,
    // Hold the previous range's chart (dimmed) while the next loads.
    placeholderData: (prev) => prev,
  });
}

// data is null when the server has no history endpoint (404).
export function useServiceUsageHistory(project: string, service: string, env: string, range: string) {
  return useQuery({
    queryKey: ["projects", project, "services", service, "usage-history", env, range],
    queryFn: () => getServiceUsageHistory(project, service, env, range),
    enabled: !!project && !!service && !!env,
    // A failure falls back to client polling; don't hold the charts on
    // the loading skeleton through several retries first.
    retry: 1,
    refetchInterval: 60_000,
    staleTime: 30_000,
    // Hold the previous range's history while the next loads, but never
    // another env's.
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[5] === env ? prev : undefined),
  });
}
