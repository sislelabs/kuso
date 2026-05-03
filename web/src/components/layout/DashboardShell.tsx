"use client";

import { type ReactNode } from "react";
import { Sidebar } from "@/components/layout/Sidebar";
import { TopNav } from "@/components/layout/TopNav";
import { CommandPalette } from "@/components/command/CommandPalette";

export function DashboardShell({ children }: { children: ReactNode }) {
  // Layout: top nav (h-12) is the persistent shell. Below it sits the
  // sidebar rail (left) + the page main area. The Sidebar is the icon
  // rail; project + env switching lives in the TopNav now.
  return (
    <div className="flex h-screen flex-col overflow-hidden bg-[var(--bg-primary)]">
      <TopNav />
      <div className="flex flex-1 overflow-hidden">
        <Sidebar />
        <main className="flex-1 overflow-y-auto bg-[var(--bg-primary)]">
          {children}
        </main>
      </div>
      <CommandPalette />
    </div>
  );
}
