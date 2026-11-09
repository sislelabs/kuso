"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { FolderKanban, Search, Settings } from "lucide-react";
import { NotificationsSheetButton } from "./Notifications";

// Bottom tab bar for phones (< 640px). The top bar keeps only the logo,
// project/breadcrumb and account; everything a person reaches for during
// an incident on a phone lives here, in thumb range.
export function MobileNav() {
  const pathname = usePathname() ?? "";
  const item =
    "group relative flex min-w-0 flex-1 flex-col items-center justify-center gap-0.5 pt-1.5 pb-1 text-[11px] font-medium text-[var(--text-tertiary)] outline-none focus-visible:text-[var(--text-primary)] aria-[current=page]:text-[var(--text-primary)] [&>svg]:h-6 [&>svg]:w-11 [&>svg]:rounded-full [&>svg]:px-2.5 [&>svg]:py-0.5 aria-[current=page]:[&>svg]:bg-[var(--bg-tertiary)] aria-[current=page]:[&>svg]:text-[var(--text-primary)]";
  const openPalette = () =>
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "k", metaKey: true, ctrlKey: true }));

  return (
    <nav
      aria-label="Primary"
      className="fixed inset-x-0 bottom-0 z-40 flex h-[calc(3.5rem+env(safe-area-inset-bottom))] border-t border-[var(--border-subtle)] bg-[var(--bg-primary)]/95 pb-[env(safe-area-inset-bottom)] backdrop-blur supports-backdrop-filter:bg-[var(--bg-primary)]/85 sm:hidden"
    >
      <Link
        href="/projects"
        aria-current={pathname.startsWith("/projects") ? "page" : undefined}
        className={item}
      >
        <FolderKanban aria-hidden  />
        Projects
      </Link>
      <button type="button" onClick={openPalette} className={item}>
        <Search aria-hidden  />
        Search
      </button>
      <NotificationsSheetButton className={item}>Alerts</NotificationsSheetButton>
      <Link
        href="/settings"
        aria-current={pathname.startsWith("/settings") ? "page" : undefined}
        className={item}
      >
        <Settings aria-hidden  />
        Settings
      </Link>
    </nav>
  );
}
