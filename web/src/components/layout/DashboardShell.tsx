"use client";

import { type ReactNode } from "react";
import { TopNav } from "@/components/layout/TopNav";
import { CommandPalette } from "@/components/command/CommandPalette";

export function DashboardShell({ children }: { children: ReactNode }) {
  // Layout: top nav (h-12) is the entire chrome. Project + env
  // switching, cluster nodes, and the user/admin menus all live there.
  // The page body gets the full viewport width below the nav.
  return (
    <div className="flex h-screen flex-col overflow-hidden bg-[var(--bg-primary)]">
      <TopNav />
      <main className="flex-1 overflow-y-auto bg-[var(--bg-primary)]">
        {children}
      </main>
      <CommandPalette />
    </div>
  );
}
