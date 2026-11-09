"use client";

import { Suspense, type ReactNode } from "react";
import { TopNav } from "@/components/layout/TopNav";
import { SkipLinks } from "@/components/layout/SkipLinks";
import { MobileNav } from "@/components/layout/MobileNav";
import { KeyboardShortcuts } from "@/components/layout/KeyboardShortcuts";
import { CommandPalette } from "@/components/command/CommandPalette";
import { ServerVersionGuard } from "@/components/layout/ServerVersionGuard";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LiveAnnouncer } from "@/components/shared/LiveAnnouncer";

export function DashboardShell({ children }: { children: ReactNode }) {
  // The top nav (h-12) is the desktop chrome; phones add a bottom tab
  // bar, so <main> reserves its height below sm. The staged-changes bar
  // sets --staged-bar-space (on phones it already includes the nav), so
  // the larger of the two wins rather than both adding up.
  //
  // Suspense boundaries: TopNav and many pages call useSearchParams,
  // which static export requires to sit under a Suspense ancestor. One
  // boundary per island so a slow page doesn't freeze the shell.
  return (
    <TooltipProvider delay={300}>
      <div className="relative flex h-dvh flex-col overflow-hidden bg-[var(--bg-primary)]">
        <SkipLinks />
        <Suspense fallback={<div className="h-12 shrink-0 border-b border-[var(--border-subtle)]" />}>
          <TopNav />
        </Suspense>
        <main
          id="main"
          tabIndex={-1}
          className="relative flex-1 overflow-y-auto [scrollbar-gutter:stable] bg-[var(--bg-primary)] pb-[max(var(--staged-bar-space,0px),calc(3.5rem+env(safe-area-inset-bottom)))] outline-none sm:pb-[var(--staged-bar-space,0px)]"
        >
          <Suspense fallback={null}>{children}</Suspense>
        </main>
        <MobileNav />
        <CommandPalette />
        <KeyboardShortcuts />
        <LiveAnnouncer />
        <Suspense fallback={null}>
          <ServerVersionGuard />
        </Suspense>
      </div>
    </TooltipProvider>
  );
}
