import { cn } from "@/lib/utils";

// StatusChip is the one shape for a small state marker next to a name:
// error counts, CVEs, "Rolled back", "Pinned", "Staged". Same pill, same
// height, tone picks the colour. Meaning never rides on colour alone:
// pass an icon (or `dot`) plus a word.
export type StatusChipTone = "neutral" | "danger" | "warning" | "success" | "info" | "building";

const TONE: Record<StatusChipTone, { chip: string; hover: string; dot: string }> = {
  neutral: {
    chip: "border-[var(--border-default)] bg-[var(--bg-tertiary)] text-[var(--text-secondary)]",
    hover: "hover:border-[var(--border-strong)] hover:text-[var(--text-primary)]",
    dot: "bg-[var(--text-tertiary)]",
  },
  danger: {
    chip: "border-[var(--error)]/35 bg-[var(--error-subtle)] text-[var(--error)]",
    hover: "hover:border-[var(--error)]/70",
    dot: "bg-[var(--error)]",
  },
  warning: {
    chip: "border-[var(--warning)]/35 bg-[var(--warning-subtle)] text-[var(--warning)]",
    hover: "hover:border-[var(--warning)]/70",
    dot: "bg-[var(--warning)]",
  },
  success: {
    chip: "border-[var(--success)]/35 bg-[var(--success-subtle)] text-[var(--success)]",
    hover: "hover:border-[var(--success)]/70",
    dot: "bg-[var(--success)]",
  },
  info: {
    chip: "border-[var(--accent)]/40 bg-[var(--accent-subtle)] text-[var(--accent)]",
    hover: "hover:border-[var(--accent)]/70",
    dot: "bg-[var(--accent)]",
  },
  building: {
    chip: "border-[var(--building)]/35 bg-[var(--building-subtle)] text-[var(--building)]",
    hover: "hover:border-[var(--building)]/70",
    dot: "bg-[var(--building)]",
  },
};

export function statusChipClass(
  tone: StatusChipTone,
  opts: { interactive?: boolean; size?: "sm" | "md" } = {},
): string {
  const t = TONE[tone];
  return cn(
    "inline-flex shrink-0 items-center gap-1 whitespace-nowrap rounded-full border font-medium tabular-nums leading-none [&>svg]:size-3 [&>svg]:shrink-0",
    opts.size === "sm" ? "h-[18px] px-1.5 text-xs" : "h-5 px-2 text-xs",
    t.chip,
    opts.interactive &&
      cn(
        "transition-colors outline-none focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[var(--focus)]",
        t.hover,
      ),
  );
}

export function StatusChip({
  tone = "neutral",
  size,
  icon: Icon,
  dot,
  className,
  children,
  ...rest
}: {
  tone?: StatusChipTone;
  size?: "sm" | "md";
  icon?: React.ComponentType<{ className?: string; "aria-hidden"?: boolean }>;
  // A tone dot instead of an icon (Staged).
  dot?: boolean;
} & React.HTMLAttributes<HTMLSpanElement>) {
  return (
    <span className={cn(statusChipClass(tone, { size }), className)} {...rest}>
      {Icon && <Icon aria-hidden />}
      {dot && !Icon && <span aria-hidden className={cn("size-1.5 shrink-0 rounded-full", TONE[tone].dot)} />}
      {children}
    </span>
  );
}
