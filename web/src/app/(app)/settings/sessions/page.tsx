"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { Clock, Infinity as InfinityIcon } from "lucide-react";

interface SessionSettings {
  ttlSeconds: number;
  neverExpire: boolean;
}

const DAY = 24 * 60 * 60;
const HOUR = 60 * 60;

// Presets the operator can click instead of doing the seconds math.
const PRESETS: { label: string; seconds: number }[] = [
  { label: "8 hours", seconds: 8 * HOUR },
  { label: "1 day", seconds: 1 * DAY },
  { label: "7 days", seconds: 7 * DAY },
  { label: "30 days", seconds: 30 * DAY },
  { label: "90 days", seconds: 90 * DAY },
];

// humanize turns a TTL in seconds into a short, friendly string for the
// "current" readout — "30 days", "8 hours", "45 minutes".
function humanize(seconds: number): string {
  if (seconds % DAY === 0) {
    const d = seconds / DAY;
    return `${d} day${d === 1 ? "" : "s"}`;
  }
  if (seconds % HOUR === 0) {
    const h = seconds / HOUR;
    return `${h} hour${h === 1 ? "" : "s"}`;
  }
  const m = Math.round(seconds / 60);
  return `${m} minute${m === 1 ? "" : "s"}`;
}

export default function SessionSettingsPage() {
  const [loaded, setLoaded] = useState(false);
  const [s, setS] = useState<SessionSettings>({
    ttlSeconds: 30 * DAY,
    neverExpire: false,
  });
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    api<SessionSettings>("/api/admin/settings/session")
      .then((d) => {
        setS(d);
        setLoaded(true);
      })
      .catch((e) => {
        toast.error(e instanceof Error ? e.message : "Failed to load settings");
        setLoaded(true);
      });
  }, []);

  const save = async () => {
    setSaving(true);
    try {
      await api("/api/admin/settings/session", { method: "PUT", body: s });
      toast.success("Saved. Applies the next time someone signs in.");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };

  if (!loaded) {
    return (
      <div className="mx-auto max-w-3xl space-y-4 p-6 lg:p-8">
        <Skeleton className="h-10 w-1/3" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  // Editing the numeric field is in hours/days via presets; for an
  // arbitrary value we expose a raw "days" number too.
  const ttlDays = s.ttlSeconds / DAY;

  return (
    <div className="mx-auto max-w-3xl space-y-8 p-6 lg:p-8">
      <header>
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Sessions</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          How long a dashboard login stays valid before kuso asks the user to
          sign in again. The change applies the <em>next</em> time someone signs
          in — existing sessions keep the lifetime they were issued with.
        </p>
      </header>

      {/* Never-expire toggle */}
      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
        <label className="flex cursor-pointer items-start gap-3">
          <input
            type="checkbox"
            checked={s.neverExpire}
            onChange={(e) => setS({ ...s, neverExpire: e.target.checked })}
            className="mt-0.5 h-4 w-4 accent-[var(--accent)]"
          />
          <span className="flex-1">
            <span className="flex items-center gap-2 text-sm font-medium">
              <InfinityIcon className="h-4 w-4 text-[var(--text-tertiary)]" />
              Never log out
            </span>
            <span className="mt-1 block text-xs text-[var(--text-secondary)]">
              Sessions stay valid until the user explicitly logs out (or an admin
              revokes access / changes their role). Convenient for a single-user
              or homelab install. Security tradeoff: a leaked session cookie stays
              valid indefinitely until revoked — only enable this if you trust
              every device that signs in.
            </span>
          </span>
        </label>
      </section>

      {/* TTL picker — disabled when never-expire is on */}
      <section
        className={
          "space-y-4 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4 " +
          (s.neverExpire ? "pointer-events-none opacity-50" : "")
        }
      >
        <div className="flex items-center gap-2">
          <Clock className="h-4 w-4 text-[var(--text-tertiary)]" />
          <h2 className="text-sm font-semibold">Session lifetime</h2>
        </div>
        <p className="text-xs text-[var(--text-secondary)]">
          Currently <span className="font-mono">{humanize(s.ttlSeconds)}</span>.
          Pick a preset or set a custom number of days below.
        </p>

        <div className="flex flex-wrap gap-2">
          {PRESETS.map((p) => {
            const active = !s.neverExpire && s.ttlSeconds === p.seconds;
            return (
              <button
                key={p.label}
                type="button"
                onClick={() => setS({ ...s, ttlSeconds: p.seconds, neverExpire: false })}
                className={
                  "rounded-md border px-3 py-1.5 text-sm transition-colors " +
                  (active
                    ? "border-[var(--accent)]/50 bg-[var(--accent)]/10 text-[var(--text-primary)]"
                    : "border-[var(--border-subtle)] bg-[var(--bg-primary)] hover:border-[var(--accent)]/40")
                }
              >
                {p.label}
              </button>
            );
          })}
        </div>

        <div className="flex items-center gap-2 pt-1">
          <label className="text-xs text-[var(--text-secondary)]">Custom:</label>
          <input
            type="number"
            min={1}
            max={365}
            step={1}
            value={Number.isInteger(ttlDays) ? ttlDays : ttlDays.toFixed(2)}
            onChange={(e) => {
              const days = parseFloat(e.target.value || "1");
              if (!Number.isNaN(days) && days > 0) {
                setS({ ...s, ttlSeconds: Math.round(days * DAY), neverExpire: false });
              }
            }}
            className="w-24 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1 font-mono text-sm"
          />
          <span className="text-xs text-[var(--text-secondary)]">days (max 365 — use “never log out” for longer)</span>
        </div>
      </section>

      <div className="flex items-center justify-between border-t border-[var(--border-subtle)] pt-4">
        <p className="text-xs text-[var(--text-tertiary)]">
          Default is 30 days. Lower it for shared/secure environments; raise it
          (or turn expiry off) to stop frequent logouts.
        </p>
        <Button onClick={save} disabled={saving}>
          {saving ? "Saving…" : "Save"}
        </Button>
      </div>
    </div>
  );
}
