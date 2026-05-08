import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

// LoadingState replaces the patchwork of "Loading…" strings, blank
// nulls, and ad-hoc skeleton blocks scattered across the app. Three
// presets cover ~95% of cases:
//
//   list     — header + 3 rows (settings tables, project list)
//   card     — small card-sized box (canvas pre-load, addon detail)
//   inline   — single short row (a chip/pill/section header)
//
// Variant `kind` is purely cosmetic — server response time decides
// when this disappears, not the kind. Pulling new pages onto this
// keeps a consistent feel.
//
// Why not just use Skeleton everywhere? Skeleton is the primitive;
// LoadingState is the composition we keep accidentally re-implementing
// at different sizes / spacings. One name, one look.
export function LoadingState({
  kind = "list",
  className,
  label,
}: {
  kind?: "list" | "card" | "inline";
  className?: string;
  label?: string;
}) {
  if (kind === "inline") {
    return (
      <div className={cn("flex items-center gap-2", className)}>
        <Skeleton className="h-3 w-24" />
        {label && (
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">{label}</span>
        )}
      </div>
    );
  }
  if (kind === "card") {
    return (
      <div className={cn("space-y-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4", className)}>
        <Skeleton className="h-4 w-1/3" />
        <Skeleton className="h-3 w-2/3" />
        <Skeleton className="h-3 w-1/2" />
      </div>
    );
  }
  return (
    <div className={cn("space-y-3", className)}>
      <Skeleton className="h-6 w-40" />
      <Skeleton className="h-4 w-full" />
      <Skeleton className="h-4 w-3/4" />
      <Skeleton className="h-4 w-2/3" />
    </div>
  );
}
