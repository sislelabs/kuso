import { ErrorState } from "./ErrorState";

// QueryErrorState is the branch a list/panel renders when its query
// failed. Without it the empty-state ternary runs on `data ?? []` and a
// backend outage reads as "nothing here" (e.g. "No errors detected").
// Thin wrapper over ErrorState's inline form; Retry only shows when the
// failure could plausibly succeed on a second try.
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
  return <ErrorState inline what={what} error={error} onRetry={onRetry} className={className} />;
}
