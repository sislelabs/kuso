"use client";

import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { CONCURRENCY_POLICIES, type CronOnFailureForm, type CronPolicy } from "./cronPatch";

const labelClass = "font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]";

const CONCURRENCY_HINT: Record<string, string> = {
  Forbid: "skip a run while the previous one is still going",
  Allow: "runs may overlap",
  Replace: "cancel the running job and start the new one",
};

// CronPolicyFields edits the run policy shared by every cron kind.
export function CronPolicyFields({
  value,
  onChange,
}: {
  value: CronPolicy;
  onChange: (next: CronPolicy) => void;
}) {
  const current = value.concurrencyPolicy || "Forbid";
  return (
    <div className="space-y-2">
      <div>
        <span className={labelClass}>if a run is still going</span>
        <div className="mt-1 flex flex-wrap items-center gap-2">
          <div role="group" aria-label="Concurrency policy" className="inline-flex items-center gap-0.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
            {CONCURRENCY_POLICIES.map((p) => (
              <button
                key={p}
                type="button"
                aria-pressed={current === p}
                onClick={() => onChange({ ...value, concurrencyPolicy: p })}
                className={cn(
                  "rounded px-1.5 py-0.5 font-mono text-[10px] transition-colors",
                  current === p
                    ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                    : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]",
                )}
              >
                {p}
              </button>
            ))}
          </div>
          <span className="text-[10px] text-[var(--text-tertiary)]">{CONCURRENCY_HINT[current]}</span>
        </div>
      </div>
      <div className="grid grid-cols-2 gap-2">
        <label className="block">
          <span className={labelClass}>timeout (s)</span>
          <Input
            type="number"
            min={0}
            value={value.activeDeadlineSeconds}
            onChange={(e) => onChange({ ...value, activeDeadlineSeconds: e.target.value })}
            placeholder="none"
            className="mt-1 h-7 font-mono text-[11px]"
          />
        </label>
        <label className="block">
          <span className={labelClass}>missed-run window (s)</span>
          <Input
            type="number"
            min={0}
            value={value.startingDeadlineSeconds}
            onChange={(e) => onChange({ ...value, startingDeadlineSeconds: e.target.value })}
            placeholder="300"
            className="mt-1 h-7 font-mono text-[11px]"
          />
        </label>
      </div>
      <p className="text-[10px] text-[var(--text-tertiary)]">
        Timeout kills a run that takes longer. The missed-run window is how late a run may
        still start after the schedule slipped (e.g. during an outage).
      </p>
    </div>
  );
}

// CronOnFailureFields edits the failure webhook of a project-scoped cron.
export function CronOnFailureFields({
  value,
  onChange,
  project,
}: {
  value: CronOnFailureForm;
  onChange: (next: CronOnFailureForm) => void;
  project: string;
}) {
  return (
    <div className="space-y-2">
      <label className="block">
        <span className={labelClass}>failure webhook</span>
        <Input
          value={value.webhookURL}
          onChange={(e) => onChange({ ...value, webhookURL: e.target.value })}
          placeholder="https://hooks.example.com/cron-failed"
          className="mt-1 h-7 font-mono text-[11px]"
          spellCheck={false}
        />
      </label>
      {value.webhookURL.trim() && (
        <div className="grid grid-cols-2 gap-2">
          <label className="block">
            <span className={labelClass}>signing secret</span>
            <Input
              value={value.secretName}
              onChange={(e) => onChange({ ...value, secretName: e.target.value })}
              placeholder={`${project}-shared`}
              className="mt-1 h-7 font-mono text-[11px]"
              spellCheck={false}
            />
          </label>
          <label className="block">
            <span className={labelClass}>key</span>
            <Input
              value={value.secretKey}
              onChange={(e) => onChange({ ...value, secretKey: e.target.value })}
              placeholder="CRON_WEBHOOK_SECRET"
              className="mt-1 h-7 font-mono text-[11px]"
              spellCheck={false}
            />
          </label>
        </div>
      )}
      <p className="text-[10px] text-[var(--text-tertiary)]">
        POSTed when a run fails. Optional HMAC signing key from {project}-shared or an addon&apos;s
        connection secret. Clear the URL to remove the webhook.
      </p>
    </div>
  );
}
