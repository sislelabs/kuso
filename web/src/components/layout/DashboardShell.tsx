"use client";

import { Suspense, type ReactNode } from "react";
import { TopNav } from "@/components/layout/TopNav";
import { CommandPalette } from "@/components/command/CommandPalette";

export function DashboardShell({ children }: { children: ReactNode }) {
  // Layout: top nav (h-12) is the entire chrome. Project + env
  // switching, cluster nodes, and the user/admin menus all live there.
  // The page body gets the full viewport width below the nav.
  //
  // Suspense boundaries:
  //   - TopNav internally calls useSearchParams (for the env switcher
  //     state). Static-export Next requires a Suspense ancestor or it
  //     bails out the whole subtree to client-only rendering.
  //   - <main> wraps the children in another boundary because
  //     individual pages also use useSearchParams (project canvas,
  //     etc.). One boundary per "island" so a slow page doesn't
  //     freeze the whole shell.
  return (
    <div className="flex h-screen flex-col overflow-hidden bg-[var(--bg-primary)]">
      <Suspense fallback={<div className="h-12 border-b" />}>
        <TopNav />
      </Suspense>
      <main className="flex-1 overflow-y-auto bg-[var(--bg-primary)]">
        <Suspense fallback={null}>{children}</Suspense>
      </main>
      <CommandPalette />
    </div>
  );
}
