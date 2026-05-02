"use client";

import { useState, type ReactNode } from "react";
import { Sidebar } from "@/components/layout/Sidebar";
import { Header } from "@/components/layout/Header";
import { MobileNav } from "@/components/layout/MobileNav";
import { useSession } from "@/features/auth";

export function DashboardShell({ children }: { children: ReactNode }) {
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const { data: session } = useSession();

  const user = session?.user ?? { name: "User", email: "", image: null };

  return (
    <div className="flex h-screen overflow-hidden">
      <Sidebar />
      <MobileNav open={mobileNavOpen} onOpenChange={setMobileNavOpen} />
      <div className="flex flex-1 flex-col overflow-hidden">
        <Header user={user} onMenuClick={() => setMobileNavOpen(true)} />
        <main className="flex-1 overflow-y-auto bg-[var(--bg-primary)]">
          {children}
        </main>
      </div>
    </div>
  );
}
