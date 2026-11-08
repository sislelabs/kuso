"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SettingsPage, SettingsPageLoading } from "@/components/settings/SettingsPage";
import { SettingsField, SettingsNotice, SettingsSection } from "@/components/settings/SettingsSection";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

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
  // A failed load leaves placeholder defaults in the form; saving them
  // would overwrite the real settings, so Save stays off until a reload.
  const [loadFailed, setLoadFailed] = useState(false);
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
        setLoadFailed(true);
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
    return <SettingsPageLoading title="Sessions" rows={1} />;
  }

  // Editing the numeric field is in hours/days via presets; for an
  // arbitrary value we expose a raw "days" number too.
  const ttlDays = s.ttlSeconds / DAY;

  return (
    <SettingsPage
      title="Sessions"
      description={
        <>
          How long a dashboard login stays valid before kuso asks the user to sign in again. Changes apply the{" "}
          <em>next</em> time someone signs in; existing sessions keep the lifetime they were issued with.
        </>
      }
    >
      {loadFailed && (
        <SettingsNotice tone="error" title="Current settings failed to load">
          Saving is disabled so placeholder values don&apos;t overwrite the real ones. Reload the page to try again.
        </SettingsNotice>
      )}

      <SettingsSection
        title="Session lifetime"
        description={
          s.neverExpire ? (
            "Sessions never expire."
          ) : (
            <>
              Currently <span className="font-medium text-[var(--text-primary)]">{humanize(s.ttlSeconds)}</span>.
            </>
          )
        }
        footerHint="Default is 30 days. Lower it for shared environments; raise it (or turn expiry off) to stop frequent logouts."
        footer={
          <Button
            size="sm"
            onClick={save}
            disabled={saving || loadFailed}
            title={loadFailed ? "Current settings failed to load. Reload the page to edit." : undefined}
          >
            {saving ? "Saving…" : "Save"}
          </Button>
        }
      >
        <label className="flex cursor-pointer items-start gap-3">
          <input
            type="checkbox"
            checked={s.neverExpire}
            onChange={(e) => setS({ ...s, neverExpire: e.target.checked })}
            className="mt-0.5 h-4 w-4 shrink-0 accent-[var(--accent)]"
          />
          <span className="min-w-0">
            <span className="block text-sm font-medium text-[var(--text-primary)]">Never log out</span>
            <span className="mt-0.5 block text-xs leading-relaxed text-[var(--text-secondary)]">
              Sessions stay valid until the user logs out or an admin revokes access. Convenient for a single-user or
              homelab install, but a leaked session cookie stays valid until revoked. Only enable this if you trust
              every device that signs in.
            </span>
          </span>
        </label>

        <div className={cn("space-y-5", s.neverExpire && "pointer-events-none opacity-50")} aria-disabled={s.neverExpire}>
          <SettingsField label="Preset" hint="Pick a common lifetime.">
            <div className="flex flex-wrap gap-2">
              {PRESETS.map((p) => {
                const active = !s.neverExpire && s.ttlSeconds === p.seconds;
                return (
                  <button
                    key={p.label}
                    type="button"
                    aria-pressed={active}
                    onClick={() => setS({ ...s, ttlSeconds: p.seconds, neverExpire: false })}
                    className={cn(
                      "rounded-sm border px-3 py-1.5 text-sm transition-colors",
                      active
                        ? "border-[var(--border-strong)] bg-[var(--bg-tertiary)] font-medium text-[var(--text-primary)]"
                        : "border-[var(--border-subtle)] bg-[var(--bg-primary)] text-[var(--text-secondary)] hover:border-[var(--border-strong)] hover:text-[var(--text-primary)]",
                    )}
                  >
                    {p.label}
                  </button>
                );
              })}
            </div>
          </SettingsField>

          <SettingsField
            label="Custom lifetime (days)"
            htmlFor="session-days"
            hint="Between 1 and 365 days. Use “Never log out” for longer."
          >
            <Input
              id="session-days"
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
              className="max-w-40 font-mono"
            />
          </SettingsField>
        </div>
      </SettingsSection>
    </SettingsPage>
  );
}
