import { cn } from "@/lib/utils";

export type DeployStatus =
  | "building"
  | "deploying"
  | "active"
  | "awaiting"
  | "sleeping"
  | "stopped"
  | "failed"
  | "crashed"
  | "unknown";

const styles: Record<DeployStatus, string> = {
  // Building/deploying ride the dedicated --building hue (yellow)
  // so they're distinct from the orange accent used for hover and
  // primary highlights. Same hue as the canvas service node's
  // animated border in the same state.
  building:
    "bg-[var(--building-subtle)] text-[var(--building)] border-[var(--building)]/30 animate-pulse",
  deploying:
    "bg-[var(--building-subtle)] text-[var(--building)] border-[var(--building)]/30 animate-pulse",
  active:
    "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/30",
  // Awaiting the first build — neutral/informational, NOT red. A
  // brand-new build-based service has no image yet (held at replicas=0),
  // so it's intentionally not running rather than broken.
  awaiting:
    "bg-sky-500/10 text-sky-600 dark:text-sky-400 border-sky-500/30",
  sleeping:
    "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]",
  // Stopped — explicit hard stop. Slate/grey so it reads as "off" and
  // is visually distinct from sleeping (muted tertiary) and failed (red).
  stopped:
    "bg-slate-500/10 text-slate-500 dark:text-slate-300 border-slate-500/30",
  failed:
    "bg-red-500/10 text-red-600 dark:text-red-400 border-red-500/30",
  crashed:
    "bg-red-500/10 text-red-600 dark:text-red-400 border-red-500/30",
  unknown:
    "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]",
};

export function DeployStatusPill({
  status,
  className,
}: {
  status: DeployStatus;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 font-mono text-[10px] uppercase tracking-wider",
        styles[status],
        className
      )}
    >
      <span className="h-1.5 w-1.5 rounded-full bg-current" />
      {status}
    </span>
  );
}
