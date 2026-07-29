"use client";

// Route-segment error boundary for the authed app. Next.js renders
// this in place of the page subtree when a render throws below the
// (app) layout — the DashboardShell + AuthGate stay mounted, so this
// is a scoped fallback (nav chrome intact) rather than a whole-page
// blank. reset() re-attempts the failed render; the Home link is the
// escape hatch when a retry keeps throwing. global-error.tsx is the
// coarser net for errors that escape the layout itself.
import { useEffect } from "react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { AlertTriangle } from "lucide-react";

export default function AppError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  useEffect(() => {
    console.error("App route error:", error);
  }, [error]);

  return (
    <div className="flex min-h-[60vh] items-center justify-center p-8">
      <div className="w-full max-w-md rounded-md border border-red-500/40 bg-[var(--bg-elevated)] p-6 text-center shadow-[var(--shadow-lg)]">
        <div className="mb-3 flex items-center justify-center gap-2">
          <AlertTriangle className="h-4 w-4 text-red-400" />
          <p className="font-mono text-[11px] uppercase tracking-widest text-[var(--text-tertiary)]">
            something broke
          </p>
        </div>
        <h1 className="text-section-heading">This view failed to render</h1>
        <p className="mt-2 break-words text-xs text-[var(--text-secondary)]">
          {error?.message || "An unexpected error occurred."}
        </p>
        {error?.digest && (
          <p className="mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
            digest: {error.digest}
          </p>
        )}
        <div className="mt-5 flex items-center justify-center gap-2">
          <Button size="sm" onClick={() => reset()}>
            Try again
          </Button>
          <Link
            href="/"
            className="inline-flex h-7 items-center rounded-sm px-3 text-[0.8rem] font-medium text-[var(--text-secondary)] hover:bg-muted hover:text-foreground"
          >
            Back home
          </Link>
        </div>
      </div>
    </div>
  );
}
