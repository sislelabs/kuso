"use client";

import { useQuery } from "@tanstack/react-query";
import { Database } from "lucide-react";
import { api } from "@/lib/api-client";
import { useCan, Perms } from "@/features/auth";
import { LoadingState } from "@/components/ui/loading-state";

interface DbStats {
  writeErrors: number;
  poolOpen: number;
  poolInUse: number;
  poolIdle: number;
}

// Control-plane database health.
//
// This tile used to read writeCount / busyCount / avgWriteWaitMs, which
// were SQLite busy-timeout counters. Those fields stopped being served
// in v0.9 when the control plane moved to Postgres (see
// internal/db/stats.go), but the tile kept asking for them and falling
// back to `?? 0` -- so it rendered a confident "Writes 0 / Busy events
// 0 / Avg latency 0 ms" under a "SQLite write contention" label on a
// Postgres cluster. All three numbers were hardcoded zeros, and the one
// field the endpoint DOES serve, writeErrors, was never shown: it sat
// at 334 while the tile displayed all-clear.
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

  const writeErrors = data?.writeErrors ?? 0;
  const poolInUse = data?.poolInUse ?? 0;
  const poolOpen = data?.poolOpen ?? 0;

  // Visual cue when a write has actually failed. Healthy kuso sits at
  // 0; any nonzero value is worth a glance.
  const tone =
    writeErrors > 0
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
          Postgres pool + write errors
        </span>
      </header>

      {isPending && <LoadingState kind="inline" />}
      {isError && (
        <p className="text-xs text-[var(--error)]">Failed to load db stats.</p>
      )}
      {data && (
        <dl className="grid grid-cols-3 gap-3 text-sm">
          <Stat
            label="Write errors"
            value={writeErrors.toLocaleString()}
            warn={writeErrors > 0}
          />
          <Stat label="Connections in use" value={poolInUse.toLocaleString()} />
          <Stat label="Pool open" value={poolOpen.toLocaleString()} />
        </dl>
      )}
      {writeErrors > 0 && (
        <p className="mt-3 text-xs text-[var(--warning)]">
          {writeErrors.toLocaleString()} write
          {writeErrors === 1 ? "" : "s"} failed since this server started.
          Check the server logs for the underlying Postgres error.
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
        className={`mt-1 font-mono text-base ${warn ? "text-[var(--warning)]" : ""}`}
      >
        {value}
      </dd>
    </div>
  );
}
