"use client";

import type { ReactNode } from "react";
import { SettingsMobileSwitcher, SettingsSidebar } from "@/components/settings/SettingsNav";

// Persistent settings chrome: grouped, searchable sidebar on lg+, a
// compact section switcher (sheet) below that. Pages render their own
// SettingsPage header inside the content column.
export default function SettingsLayout({ children }: { children: ReactNode }) {
  return (
    <div className="mx-auto flex w-full max-w-[1240px] gap-10 px-4 sm:px-6 lg:px-8">
      <aside className="sticky top-0 hidden h-[calc(100dvh-3rem)] w-56 shrink-0 self-start overflow-y-auto pb-8 pt-8 lg:block">
        <SettingsSidebar />
      </aside>
      <div className="min-w-0 flex-1">
        <SettingsMobileSwitcher />
        {children}
      </div>
    </div>
  );
}
