import { cn } from "@/lib/utils";

// StagedChip marks an edit that's held for the next deploy, not live yet.
export function StagedChip({ className, label = "Staged" }: { className?: string; label?: string }) {
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center gap-1 rounded-full border border-[var(--accent)]/40 bg-[var(--accent-subtle)] px-2 py-px text-xs font-medium text-[var(--accent)]",
        className,
      )}
    >
      <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-[var(--accent)]" />
      {label}
    </span>
  );
}

// ImmediateNote labels a control that skips staging ("applies immediately").
export function ImmediateNote({ className }: { className?: string }) {
  return <span className={cn("text-xs text-[var(--text-tertiary)]", className)}>Applies immediately</span>;
}
