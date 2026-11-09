import { StatusChip } from "@/components/ui/status-chip";
import { cn } from "@/lib/utils";

// StagedChip marks an edit that's held for the next deploy, not live yet.
export function StagedChip({ className, label = "Staged" }: { className?: string; label?: string }) {
  return (
    <StatusChip tone="info" dot className={className}>
      {label}
    </StatusChip>
  );
}

// ImmediateNote labels a control that skips staging ("applies immediately").
export function ImmediateNote({ className }: { className?: string }) {
  return <span className={cn("text-xs text-[var(--text-tertiary)]", className)}>Applies immediately</span>;
}
