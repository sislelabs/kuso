"use client";

import { Fragment } from "react";
import { usePathname } from "next/navigation";
import Link from "next/link";
import { LogOut, Menu, Settings as SettingsIcon, Bell, Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { ThemeToggle } from "@/components/shared/ThemeToggle";
import { useSignOut } from "@/features/auth";
import { cn } from "@/lib/utils";

interface HeaderProps {
  user: { name: string; email: string; image?: string | null };
  onMenuClick?: () => void;
}

function prettySegment(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

export function Header({ user, onMenuClick }: HeaderProps) {
  const pathname = usePathname();
  const segments = pathname.split("/").filter(Boolean);
  const signOut = useSignOut();

  return (
    <header className="sticky top-0 z-40 flex h-14 items-center gap-4 border-b border-[var(--border-subtle)] bg-[var(--bg-primary)] px-4 lg:px-6">
      <Button
        variant="ghost"
        size="icon"
        className="lg:hidden"
        onClick={onMenuClick}
        aria-label="Open menu"
      >
        <Menu className="h-5 w-5" />
      </Button>

      <div className="hidden sm:flex items-center gap-1.5 text-sm">
        {segments.length === 0 ? (
          <span className="text-[var(--text-secondary)]">Home</span>
        ) : (
          segments.map((segment, i) => (
            <Fragment key={i}>
              {i > 0 && <span className="text-[var(--text-tertiary)]">/</span>}
              <span
                className={cn(
                  i === segments.length - 1
                    ? "text-[var(--text-primary)] font-medium"
                    : "text-[var(--text-secondary)]"
                )}
              >
                {prettySegment(segment)}
              </span>
            </Fragment>
          ))
        )}
      </div>

      <div className="flex-1" />

      <button
        type="button"
        className="hidden sm:inline-flex items-center gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] px-3 py-1.5 text-xs text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-secondary)] transition-colors"
        aria-label="Open command palette"
      >
        <Search className="h-3.5 w-3.5" />
        <span>Search</span>
        <kbd className="ml-2 hidden rounded border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)] sm:inline">
          ⌘K
        </kbd>
      </button>

      <Button variant="ghost" size="icon" aria-label="Notifications" disabled>
        <Bell className="h-4 w-4" />
      </Button>

      <ThemeToggle />

      <DropdownMenu>
        <DropdownMenuTrigger>
          <Avatar className="h-8 w-8 cursor-pointer">
            <AvatarImage src={user.image ?? undefined} alt={user.name} />
            <AvatarFallback>
              {user.name?.[0]?.toUpperCase() ?? "U"}
            </AvatarFallback>
          </Avatar>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-56">
          <div className="px-2 py-1.5">
            <p className="text-sm font-medium text-[var(--text-primary)]">{user.name}</p>
            <p className="text-xs text-[var(--text-secondary)]">{user.email}</p>
          </div>
          <DropdownMenuSeparator />
          <DropdownMenuItem>
            <Link href="/settings/profile" className="flex items-center w-full">
              <SettingsIcon className="mr-2 h-4 w-4" />
              Profile
            </Link>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={signOut}>
            <LogOut className="mr-2 h-4 w-4" />
            Sign out
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </header>
  );
}
