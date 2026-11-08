import { cn } from "@/lib/utils";
import type { Severity } from "@/features/vulns";

// Literal class strings so Tailwind's scanner picks them up.
export const SEVERITY_TONE: Record<Severity, { text: string; bg: string; border: string }> = {
  CRITICAL: {
    text: "text-[var(--sev-critical)]",
    bg: "bg-[var(--sev-critical-subtle)]",
    border: "border-[var(--sev-critical)]/40",
  },
  HIGH: {
    text: "text-[var(--sev-high)]",
    bg: "bg-[var(--sev-high-subtle)]",
    border: "border-[var(--sev-high)]/40",
  },
  MEDIUM: {
    text: "text-[var(--sev-medium)]",
    bg: "bg-[var(--sev-medium-subtle)]",
    border: "border-[var(--sev-medium)]/40",
  },
  LOW: {
    text: "text-[var(--sev-low)]",
    bg: "bg-[var(--sev-low-subtle)]",
    border: "border-[var(--sev-low)]/40",
  },
  UNKNOWN: {
    text: "text-[var(--sev-unknown)]",
    bg: "bg-[var(--sev-unknown-subtle)]",
    border: "border-[var(--sev-unknown)]/40",
  },
};

export function SeverityBadge({ severity, className }: { severity: Severity; className?: string }) {
  const tone = SEVERITY_TONE[severity] ?? SEVERITY_TONE.UNKNOWN;
  return (
    <span
      className={cn(
        "inline-flex items-center rounded border px-1.5 py-px text-xs font-semibold",
        tone.text,
        tone.bg,
        tone.border,
        className,
      )}
    >
      {severity.charAt(0) + severity.slice(1).toLowerCase()}
    </span>
  );
}
