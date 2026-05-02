import { cn } from "@/lib/utils";

export type DeployStatus =
  | "building"
  | "deploying"
  | "active"
  | "sleeping"
  | "failed"
  | "crashed"
  | "unknown";

const styles: Record<DeployStatus, string> = {
  building:
    "bg-[var(--accent-subtle)] text-[var(--accent)] border-[var(--accent)]/30 animate-pulse",
  deploying:
    "bg-[var(--accent-subtle)] text-[var(--accent)] border-[var(--accent)]/30 animate-pulse",
  active:
    "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/30",
  sleeping:
    "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] border-[var(--border-subtle)]",
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
