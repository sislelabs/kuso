import { cn } from "@/lib/utils";
import { Moon } from "lucide-react";

export function SleepBadge({
  duration,
  className,
}: {
  duration?: string;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-tertiary)] px-2 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)]",
        className
      )}
    >
      <Moon className="h-3 w-3" />
      <span>asleep{duration ? ` · ${duration}` : ""}</span>
    </span>
  );
}
