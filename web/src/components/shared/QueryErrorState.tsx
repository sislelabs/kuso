import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

// QueryErrorState is the branch a list/panel renders when its query
// failed. Without it the empty-state ternary runs on `data ?? []` and a
// backend outage reads as "nothing here" (e.g. "No errors detected").
export function QueryErrorState({
  what,
  error,
  onRetry,
  className,
}: {
  what: string;
  error: unknown;
  onRetry?: () => void;
  className?: string;
}) {
  const detail = error instanceof Error && error.message ? error.message : "unknown error";
  return (
    <div
      role="alert"
      className={cn(
        "flex items-start gap-3 rounded-md border border-[var(--error)]/30 bg-[var(--error-subtle)] p-4 text-sm",
        className,
      )}
    >
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-[var(--error)]" />
      <div className="min-w-0 flex-1">
        <p className="font-medium text-[var(--text-primary)]">Couldn&apos;t load {what}</p>
        <p className="mt-0.5 break-words font-mono text-xs text-[var(--text-secondary)]">{detail}</p>
      </div>
      {onRetry && (
        <Button variant="outline" size="sm" onClick={onRetry}>
          Retry
        </Button>
      )}
    </div>
  );
}
