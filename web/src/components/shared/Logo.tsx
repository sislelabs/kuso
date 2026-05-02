import { cn } from "@/lib/utils";

export function Logo({
  showText = true,
  className,
}: {
  showText?: boolean;
  className?: string;
}) {
  return (
    <span className={cn("inline-flex items-center gap-2", className)}>
      <span
        aria-hidden
        className="inline-block h-6 w-6 rounded-md"
        style={{
          background:
            "linear-gradient(135deg, var(--accent), var(--accent-hover))",
        }}
      />
      {showText && (
        <span className="font-heading text-base font-semibold tracking-tight text-[var(--text-primary)]">
          kuso
        </span>
      )}
    </span>
  );
}
