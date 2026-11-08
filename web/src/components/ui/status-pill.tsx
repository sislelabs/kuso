import { cn } from "@/lib/utils";
import { TONE_CLASSES, type StatusMeta } from "@/lib/status";

// StatusPill renders one entry of the status vocabulary (lib/status.ts):
// a tone-coloured dot plus the canonical label. `detail` is the cause or
// count shown in the tooltip and to screen readers.
export function StatusPill({
  status,
  detail,
  variant = "outline",
  className,
}: {
  status: StatusMeta;
  detail?: string;
  // outline: neutral border, coloured dot (headers). tinted: tone fill.
  variant?: "outline" | "tinted";
  className?: string;
}) {
  const tone = TONE_CLASSES[status.tone];
  return (
    <span
      title={detail || status.label}
      className={cn(
        "inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium",
        variant === "tinted" ? tone.chip : "border-[var(--border-subtle)] text-[var(--text-secondary)]",
        className,
      )}
    >
      <StatusDot status={status} />
      {status.label}
      {detail && <span className="sr-only">: {detail}</span>}
    </span>
  );
}

export function StatusDot({ status, className }: { status: StatusMeta; className?: string }) {
  const dot = TONE_CLASSES[status.tone].dot;
  return (
    <span aria-hidden className={cn("relative inline-flex size-2 shrink-0", className)}>
      {status.pulse && <span className={cn("absolute inset-0 rounded-full opacity-60 motion-safe:animate-ping", dot)} />}
      <span className={cn("relative inline-block size-2 rounded-full", dot)} />
    </span>
  );
}
