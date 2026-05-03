"use client";

import { useState } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useRouteParams } from "@/lib/dynamic-params";
import {
  LayoutGrid,
  Activity,
  Terminal,
  Settings,
  ChevronsLeft,
  ChevronsRight,
  Plus,
  User,
  KeyRound,
  Users,
  Shield,
  UsersRound,
  Bell,
  Cog,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { Logo } from "@/components/shared/Logo";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { useSession } from "@/features/auth";
import { useProjects } from "@/features/projects";
import type { KusoProject } from "@/types/projects";
import { ThemeToggle } from "@/components/shared/ThemeToggle";

interface NavItem {
  name: string;
  href: string;
  icon: React.ComponentType<{ className?: string }>;
  requiredPermission?: string;
}

function projectHealthDot(project: KusoProject): string {
  // Until we have services-rolled-up status from a single endpoint, use a
  // neutral gray dot. Phase B places-holds the visual; Phase C populates.
  // The prop is wired through so we can light it up the moment the data
  // exists.
  void project;
  return "bg-[var(--text-tertiary)]/50";
}

export function Sidebar() {
  const pathname = usePathname();
  const params = useRouteParams<{ project: string }>(["project"]);
  const currentProject = params.project;
  const [collapsed, setCollapsed] = useState(false);
  const { data: session } = useSession();
  const projects = useProjects();
  const user = session?.user;
  const initial = (user?.name?.[0] ?? user?.email?.[0] ?? "U").toUpperCase();
  const perms = session?.session.permissions ?? [];

  const projectNav: NavItem[] = currentProject
    ? [
        { name: "Canvas", href: `/projects/${currentProject}`, icon: LayoutGrid },
        { name: "Activity", href: `/projects/${currentProject}/activity`, icon: Activity },
        { name: "Logs", href: `/projects/${currentProject}/logs`, icon: Terminal },
        { name: "Settings", href: `/projects/${currentProject}/settings`, icon: Settings },
      ]
    : [];

  const accountNav: NavItem[] = [
    { name: "Profile", href: "/settings/profile", icon: User },
    { name: "Tokens", href: "/settings/tokens", icon: KeyRound },
    { name: "Notifications", href: "/settings/notifications", icon: Bell },
    { name: "Config", href: "/settings/config", icon: Cog, requiredPermission: "config:read" },
    { name: "Users", href: "/settings/users", icon: Users, requiredPermission: "user:write" },
    { name: "Roles", href: "/settings/roles", icon: Shield, requiredPermission: "user:write" },
    { name: "Groups", href: "/settings/groups", icon: UsersRound, requiredPermission: "user:write" },
  ];

  const filteredAccount = accountNav.filter(
    (n) => !n.requiredPermission || perms.includes(n.requiredPermission)
  );

  return (
    <aside
      className={cn(
        "hidden lg:flex lg:flex-col border-r border-[var(--border-subtle)] bg-[var(--bg-secondary)] transition-all duration-300",
        collapsed ? "lg:w-16" : "lg:w-[260px]"
      )}
    >
      <div
        className={cn(
          "flex h-14 items-center border-b border-[var(--border-subtle)]",
          collapsed ? "justify-center px-2" : "px-5"
        )}
      >
        <Link href="/projects">
          <Logo showText={!collapsed} />
        </Link>
      </div>

      <nav className="flex-1 overflow-y-auto p-3 space-y-6">
        <div>
          {!collapsed && (
            <h3 className="mb-2 px-3 text-[10px] font-semibold text-[var(--text-tertiary)] uppercase tracking-[0.08em]">
              Projects
            </h3>
          )}
          <ul className="space-y-0.5">
            {(projects.data ?? []).map((p) => {
              const name = p.metadata.name;
              const isActive = currentProject === name;
              return (
                <li key={p.metadata.uid ?? name}>
                  <Link
                    href={`/projects/${name}`}
                    title={collapsed ? name : undefined}
                    className={cn(
                      "relative flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-all",
                      collapsed && "justify-center px-2",
                      isActive
                        ? "bg-[var(--accent-subtle)] text-[var(--text-primary)] before:absolute before:left-0 before:top-1.5 before:bottom-1.5 before:w-[2px] before:rounded-full before:bg-[var(--accent)]"
                        : "text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                    )}
                  >
                    <span className={cn("h-2 w-2 shrink-0 rounded-full", projectHealthDot(p))} />
                    {!collapsed && <span className="truncate">{name}</span>}
                  </Link>
                </li>
              );
            })}
            <li>
              <Link
                href="/projects/new"
                title={collapsed ? "New project" : undefined}
                className={cn(
                  "mt-1 flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-all text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]",
                  collapsed && "justify-center px-2"
                )}
              >
                <Plus className="h-[18px] w-[18px] shrink-0" />
                {!collapsed && "New project"}
              </Link>
            </li>
          </ul>
        </div>

        {projectNav.length > 0 && (
          <div>
            {!collapsed && (
              <h3 className="mb-2 px-3 text-[10px] font-semibold text-[var(--text-tertiary)] uppercase tracking-[0.08em]">
                {currentProject}
              </h3>
            )}
            <ul className="space-y-0.5">
              {projectNav.map((item) => {
                const isActive = pathname === item.href;
                return (
                  <li key={item.href}>
                    <Link
                      href={item.href}
                      title={collapsed ? item.name : undefined}
                      className={cn(
                        "relative flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-all",
                        collapsed && "justify-center px-2",
                        isActive
                          ? "bg-[var(--accent-subtle)] text-[var(--text-primary)] before:absolute before:left-0 before:top-1.5 before:bottom-1.5 before:w-[2px] before:rounded-full before:bg-[var(--accent)]"
                          : "text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                      )}
                    >
                      <item.icon className="h-[18px] w-[18px] shrink-0" />
                      {!collapsed && item.name}
                    </Link>
                  </li>
                );
              })}
            </ul>
          </div>
        )}

        <div>
          {!collapsed && (
            <h3 className="mb-2 px-3 text-[10px] font-semibold text-[var(--text-tertiary)] uppercase tracking-[0.08em]">
              Account
            </h3>
          )}
          <ul className="space-y-0.5">
            {filteredAccount.map((item) => {
              const isActive = pathname === item.href;
              return (
                <li key={item.href}>
                  <Link
                    href={item.href}
                    title={collapsed ? item.name : undefined}
                    className={cn(
                      "relative flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-all",
                      collapsed && "justify-center px-2",
                      isActive
                        ? "bg-[var(--accent-subtle)] text-[var(--text-primary)] before:absolute before:left-0 before:top-1.5 before:bottom-1.5 before:w-[2px] before:rounded-full before:bg-[var(--accent)]"
                        : "text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                    )}
                  >
                    <item.icon className="h-[18px] w-[18px] shrink-0" />
                    {!collapsed && item.name}
                  </Link>
                </li>
              );
            })}
          </ul>
        </div>
      </nav>

      {user && (
        <div
          className={cn(
            "flex items-center gap-3 border-t border-[var(--border-subtle)] px-3 py-3",
            collapsed && "justify-center px-2"
          )}
        >
          <Link
            href="/settings/profile"
            className="flex items-center gap-3 min-w-0 flex-1 rounded-md px-1 py-1 hover:bg-[var(--bg-tertiary)] transition-colors"
            title={collapsed ? user.name : undefined}
          >
            <Avatar className="size-8 shrink-0 border border-[var(--border-subtle)]">
              {user.image ? <AvatarImage src={user.image} alt={user.name} /> : null}
              <AvatarFallback className="bg-[var(--bg-tertiary)] text-xs font-medium">
                {initial}
              </AvatarFallback>
            </Avatar>
            {!collapsed && (
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium text-[var(--text-primary)]">
                  {user.name || "You"}
                </div>
                <div className="truncate font-mono text-[11px] text-[var(--text-tertiary)]">
                  {user.email}
                </div>
              </div>
            )}
          </Link>
          {!collapsed && <ThemeToggle />}
        </div>
      )}

      <button
        onClick={() => setCollapsed(!collapsed)}
        className="flex items-center justify-center h-10 border-t border-[var(--border-subtle)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)] transition-colors cursor-pointer"
        aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
      >
        {collapsed ? (
          <ChevronsRight className="h-4 w-4" />
        ) : (
          <ChevronsLeft className="h-4 w-4" />
        )}
      </button>
    </aside>
  );
}
