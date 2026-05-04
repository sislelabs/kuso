"use client";

import { useQuery } from "@tanstack/react-query";
import { Database } from "lucide-react";
import { api } from "@/lib/api-client";
import { useCan, Perms } from "@/features/auth";

interface DbStats {
  writeCount: number;
  busyCount: number;
  writeWaitMs: number;
  busyWaitMs: number;
  avgWriteWaitMs: number;
}

// SQLite write-lock health. Single-writer DB serializes every mutation
// behind a busy_timeout (default 5s); each busy tick is a request that
// burned the full timeout and returned an error to the user. Surfacing
// the count here lets ops spot saturation before the bug reports start.
//
// Admin-only. Hidden for non-admins (the endpoint 403s anyway, but the
// tile would render an empty error which is noise).
export function DbHealthTile() {
  const isAdmin = useCan(Perms.SettingsAdmin);

  const { data, isPending, isError } = useQuery({
    queryKey: ["admin", "db", "stats"],
    queryFn: () => api<DbStats>("/api/admin/db/stats"),
    refetchInterval: 30_000,
    enabled: isAdmin,
  });

  if (!isAdmin) return null;

  const busy = data?.busyCount ?? 0;
  const writes = data?.writeCount ?? 0;
  const avgMs = data?.avgWriteWaitMs ?? 0;

  // Visual cue when busy is nonzero. Single-box kuso should sit at 0
  // forever in normal operation; any tick is worth a glance.
  const tone =
    busy > 0
      ? "border-amber-500/40 bg-amber-500/5"
      : "border-[var(--border)] bg-[var(--surface)]";

  return (
    <section
      className={`mt-6 rounded-lg border p-4 ${tone}`}
      aria-label="Database health"
    >
      <header className="mb-2 flex items-center gap-2">
        <Database className="h-4 w-4 text-[var(--text-tertiary)]" />
        <h2 className="text-sm font-medium">Database health</h2>
        <span className="text-xs text-[var(--text-tertiary)]">
          SQLite write contention
        </span>
      </header>

      {isPending && <p className="text-xs text-[var(--text-tertiary)]">Loading…</p>}
      {isError && (
        <p className="text-xs text-red-400">Failed to load db stats.</p>
      )}
      {data && (
        <dl className="grid grid-cols-3 gap-3 text-sm">
          <Stat label="Writes" value={writes.toLocaleString()} />
          <Stat
            label="Busy events"
            value={busy.toLocaleString()}
            warn={busy > 0}
          />
          <Stat label="Avg latency" value={`${avgMs} ms`} />
        </dl>
      )}
      {busy > 0 && (
        <p className="mt-3 text-xs text-amber-400">
          {busy} write{busy === 1 ? "" : "s"} hit the busy timeout. Sustained
          contention indicates the single-writer SQLite is saturated.
        </p>
      )}
    </section>
  );
}

function Stat({ label, value, warn }: { label: string; value: string; warn?: boolean }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-[var(--text-tertiary)]">
        {label}
      </dt>
      <dd
        className={`mt-1 font-mono text-base ${warn ? "text-amber-400" : ""}`}
      >
        {value}
      </dd>
    </div>
  );
}
