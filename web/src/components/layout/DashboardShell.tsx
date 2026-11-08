"use client";

import { Suspense, type ReactNode } from "react";
import { TopNav } from "@/components/layout/TopNav";
import { SkipLinks } from "@/components/layout/SkipLinks";
import { MobileNav } from "@/components/layout/MobileNav";
import { KeyboardShortcuts } from "@/components/layout/KeyboardShortcuts";
import { CommandPalette } from "@/components/command/CommandPalette";
import { ServerVersionGuard } from "@/components/layout/ServerVersionGuard";
import { TooltipProvider } from "@/components/ui/tooltip";

export function DashboardShell({ children }: { children: ReactNode }) {
  // The top nav (h-12) is the desktop chrome; phones add a bottom tab
  // bar, so <main> reserves its height below sm.
  //
  // Suspense boundaries: TopNav and many pages call useSearchParams,
  // which static export requires to sit under a Suspense ancestor. One
  // boundary per island so a slow page doesn't freeze the shell.
  return (
    <TooltipProvider delay={300}>
      <div className="flex h-dvh flex-col overflow-hidden bg-[var(--bg-primary)]">
        <SkipLinks />
        <Suspense fallback={<div className="h-12 shrink-0 border-b border-[var(--border-subtle)]" />}>
          <TopNav />
        </Suspense>
        <main
          id="main"
          tabIndex={-1}
          className="flex-1 overflow-y-auto bg-[var(--bg-primary)] pb-[calc(3.5rem+env(safe-area-inset-bottom))] outline-none sm:pb-0"
        >
          <Suspense fallback={null}>{children}</Suspense>
        </main>
        <MobileNav />
        <CommandPalette />
        <KeyboardShortcuts />
        <Suspense fallback={null}>
          <ServerVersionGuard />
        </Suspense>
      </div>
    </TooltipProvider>
  );
}
