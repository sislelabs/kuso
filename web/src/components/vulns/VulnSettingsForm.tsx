"use client";

import { Select } from "@/components/ui/select";
import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useSaveVulnScanSettings, type VulnMode, type VulnScanSettings } from "@/features/vulns";

const MODES: { id: VulnMode; label: string; hint: string }[] = [
  {
    id: "enforce",
    label: "Enforce",
    hint: "Scan every build and block the deploy when the image has fixable critical vulnerabilities. The previous image keeps serving. If the scanner fails or takes over 15 minutes, the deploy goes ahead unscanned.",
  },
  {
    id: "report",
    label: "Report",
    hint: "Scan and notify, never block a deploy.",
  },
  {
    id: "off",
    label: "Off",
    hint: "No scans at all: no deploy gate, no nightly sweep.",
  },
];

const HOURS = Array.from({ length: 24 }, (_, h) => h);

function same(a: VulnScanSettings, b: VulnScanSettings): boolean {
  return (
    a.mode === b.mode &&
    a.scanAddons === b.scanAddons &&
    a.scanPlatform === b.scanPlatform &&
    a.nightlyHourUTC === b.nightlyHourUTC &&
    a.notifyMinSeverity === b.notifyMinSeverity &&
    a.autoUpdateAddons === b.autoUpdateAddons
  );
}

// Mount only with loaded settings: the form never holds placeholder
// defaults, so a failed load can't be saved over the real values.
export function VulnSettingsForm({ initial }: { initial: VulnScanSettings }) {
  const [baseline, setBaseline] = useState(initial);
  const [draft, setDraft] = useState(initial);
  const save = useSaveVulnScanSettings();
  const dirty = !same(draft, baseline);
  const set = <K extends keyof VulnScanSettings>(k: K, v: VulnScanSettings[K]) => setDraft((d) => ({ ...d, [k]: v }));

  const submit = () =>
    save.mutate(draft, {
      onSuccess: (saved) => {
        setBaseline(saved);
        setDraft(saved);
        toast.success("Vulnerability scanning settings saved");
      },
    });

  return (
    <section className="space-y-5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
      <fieldset className="space-y-2">
        <legend className="mb-2 text-sm font-semibold">Mode</legend>
        {MODES.map((m) => (
          <label
            key={m.id}
            className={cn(
              "flex cursor-pointer items-start gap-3 rounded-md border p-3",
              draft.mode === m.id
                ? "border-[var(--border-strong)] bg-[var(--bg-primary)]"
                : "border-[var(--border-subtle)]",
            )}
          >
            <input
              type="radio"
              name="vulnscan-mode"
              value={m.id}
              checked={draft.mode === m.id}
              onChange={() => set("mode", m.id)}
              className="mt-0.5 accent-[var(--accent)]"
            />
            <span>
              <span className="text-sm font-medium">{m.label}</span>
              <span className="mt-0.5 block text-xs text-[var(--text-secondary)]">{m.hint}</span>
            </span>
          </label>
        ))}
        <p className="text-xs text-[var(--text-tertiary)]">
          Services can override this with their own policy on the service&apos;s Vulnerabilities tab.
        </p>
      </fieldset>

      <div className={cn("space-y-3", draft.mode === "off" && "pointer-events-none opacity-50")}>
        <label className="flex items-center gap-3 text-sm">
          <input
            type="checkbox"
            checked={draft.scanAddons}
            onChange={(e) => set("scanAddons", e.target.checked)}
            className="h-4 w-4 accent-[var(--accent)]"
          />
          Scan addon images nightly (postgres, redis, …)
        </label>
        <label className={cn("flex items-start gap-3 text-sm", !draft.scanAddons && "opacity-50")}>
          <input
            type="checkbox"
            checked={draft.autoUpdateAddons && draft.scanAddons}
            disabled={!draft.scanAddons}
            onChange={(e) => set("autoUpdateAddons", e.target.checked)}
            className="mt-0.5 h-4 w-4 accent-[var(--accent)]"
          />
          <span>
            Auto-update addon images
            <span className="mt-0.5 block text-xs text-[var(--text-secondary)]">
              Moves each addon onto the newest build of its version (postgres:16 stays 16) in the 4 hours after the
              nightly sweep, once that build has been out for 3 days, is scanned and has no more fixable critical/high
              findings. Only images published by the upstream project roll on their own; third-party images (mailpit,
              MinIO) wait for Update now. Addons restart one or two at a time; a build that doesn&apos;t become ready
              within 30 minutes is rolled back.
            </span>
          </span>
        </label>
        <label className="flex items-center gap-3 text-sm">
          <input
            type="checkbox"
            checked={draft.scanPlatform}
            onChange={(e) => set("scanPlatform", e.target.checked)}
            className="h-4 w-4 accent-[var(--accent)]"
          />
          Scan platform images nightly (kuso, operator, traefik, …)
        </label>
        <label className="flex items-center gap-3 text-sm">
          Nightly sweep at
          <Select
            size="sm"
            value={draft.nightlyHourUTC}
            onChange={(e) => set("nightlyHourUTC", Number(e.target.value))}
            className="tabular-nums"
            wrapperClassName="w-auto"
          >
            {HOURS.map((h) => (
              <option key={h} value={h}>
                {String(h).padStart(2, "0")}:00
              </option>
            ))}
          </Select>
          UTC
        </label>
        <label className="flex items-center gap-3 text-sm">
          Notify on new fixable
          <Select
            size="sm"
            value={draft.notifyMinSeverity}
            onChange={(e) => set("notifyMinSeverity", e.target.value as VulnScanSettings["notifyMinSeverity"])}
            wrapperClassName="w-auto"
          >
            <option value="critical">Critical only</option>
            <option value="high">Critical and high</option>
            <option value="none">Nothing (no alerts)</option>
          </Select>
          vulnerabilities
        </label>
      </div>

      <div className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] pt-4">
        {dirty && (
          <Button variant="outline" onClick={() => setDraft(baseline)} disabled={save.isPending}>
            Discard
          </Button>
        )}
        <Button
          onClick={submit}
          disabled={!dirty || save.isPending}
          disabledReason={!dirty && !save.isPending ? "No changes to save" : undefined}
        >
          {save.isPending ? "Saving…" : "Save"}
        </Button>
      </div>
    </section>
  );
}
