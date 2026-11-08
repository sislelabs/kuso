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
    "flex min-w-0 flex-1 flex-col items-center justify-center gap-0.5 pt-1.5 pb-1 text-xs font-medium text-[var(--text-tertiary)] outline-none focus-visible:text-[var(--text-primary)] aria-[current=page]:text-[var(--text-primary)]";
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
        <FolderKanban aria-hidden className="size-6 p-0.5" />
        Projects
      </Link>
      <button type="button" onClick={openPalette} className={item}>
        <Search aria-hidden className="size-6 p-0.5" />
        Search
      </button>
      <NotificationsSheetButton className={item}>Alerts</NotificationsSheetButton>
      <Link
        href="/settings"
        aria-current={pathname.startsWith("/settings") ? "page" : undefined}
        className={item}
      >
        <Settings aria-hidden className="size-6 p-0.5" />
        Settings
      </Link>
    </nav>
  );
}
