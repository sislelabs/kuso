"use client";

import { useState } from "react";
import { cn } from "@/lib/utils";

const RANGES = ["1h", "6h", "1d", "7d", "30d"] as const;
type Range = (typeof RANGES)[number];

interface Props {
  project: string;
  service: string;
}

export function ServiceMetricsPanel({ project, service }: Props) {
  void project;
  void service;
  const [range, setRange] = useState<Range>("1h");

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="font-heading text-sm font-semibold tracking-tight">
          Traffic & runtime
        </h3>
        <div className="inline-flex rounded-md border border-[var(--border-subtle)] p-0.5 text-xs">
          {RANGES.map((r) => (
            <button
              key={r}
              type="button"
              onClick={() => setRange(r)}
              className={cn(
                "px-2 py-1 rounded font-mono",
                range === r
                  ? "bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                  : "text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
              )}
            >
              {r}
            </button>
          ))}
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <MetricCard
          title="Requests"
          empty="No request metrics yet — they appear once the service receives traffic."
        />
        <MetricCard
          title="Error rate"
          empty="No errors recorded in this window."
        />
        <MetricCard
          title="Response time"
          empty="No response-time samples yet."
        />
        <MetricCard
          title="CPU + memory"
          empty="Resource usage will populate once the pod has been running for a few minutes."
        />
      </div>
    </div>
  );
}

function MetricCard({ title, empty }: { title: string; empty: string }) {
  return (
    <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
      <h4 className="text-sm font-medium">{title}</h4>
      <div className="mt-3 flex h-32 items-center justify-center rounded bg-[var(--bg-primary)] px-3 text-center text-xs text-[var(--text-tertiary)]">
        {empty}
      </div>
    </div>
  );
}
