"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { getEnvPodMetrics, getEnvTimeseries } from "./api";
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
