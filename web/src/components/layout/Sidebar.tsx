"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useRouteParams } from "@/lib/dynamic-params";
import { useSession } from "@/features/auth";
import {
  LayoutGrid,
  Settings,
  User as UserIcon,
  KeyRound,
  Users,
  Shield,
  UsersRound,
  Bell,
  Cog,
  Server,
} from "lucide-react";
import { cn } from "@/lib/utils";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";

interface NavItem {
  name: string;
  href: string;
  icon: React.ComponentType<{ className?: string }>;
  requiredPermission?: string;
}

// Sidebar is a thin icon rail pinned to the left edge. Each row is a
// 40×40 hit target with a tooltip on hover. We don't expand to a wide
// drawer — the project + env switching lives in the TopNav now, so the
// sidebar's job is reduced to "jump between project sections" plus the
// account bucket. Stays consistent across pages so muscle memory works.
export function Sidebar() {
  const pathname = usePathname();
  const params = useRouteParams<{ project: string }>(["project"]);
  const currentProject = params.project;
  const { data: session } = useSession();
  const perms = session?.session.permissions ?? [];

  const projectNav: NavItem[] = currentProject
    ? [
        { name: "Canvas", href: `/projects/${currentProject}`, icon: LayoutGrid },
        { name: "Project settings", href: `/projects/${currentProject}/settings`, icon: Settings },
      ]
    : [];

  const accountNav: NavItem[] = [
    { name: "Profile", href: "/settings/profile", icon: UserIcon },
    { name: "API tokens", href: "/settings/tokens", icon: KeyRound },
    { name: "Notifications", href: "/settings/notifications", icon: Bell },
    { name: "Cluster nodes", href: "/settings/nodes", icon: Server },
    { name: "Cluster config", href: "/settings/config", icon: Cog, requiredPermission: "config:read" },
    { name: "Users", href: "/settings/users", icon: Users, requiredPermission: "user:write" },
    { name: "Roles", href: "/settings/roles", icon: Shield, requiredPermission: "user:write" },
    { name: "Groups", href: "/settings/groups", icon: UsersRound, requiredPermission: "user:write" },
  ];

  const filteredAccount = accountNav.filter(
    (n) => !n.requiredPermission || perms.includes(n.requiredPermission)
  );

  return (
    <TooltipProvider>
      <aside
        aria-label="Primary navigation"
        className="hidden lg:flex w-12 shrink-0 flex-col items-center gap-1 border-r border-[var(--border-subtle)] bg-[var(--bg-secondary)] py-2"
      >
        {projectNav.length > 0 && (
          <>
            <NavSection label="Project">
              {projectNav.map((n) => (
                <NavRailButton key={n.href} item={n} pathname={pathname} />
              ))}
            </NavSection>
            <Divider />
          </>
        )}

        <NavSection label="Account" className="mt-auto">
          {filteredAccount.map((n) => (
            <NavRailButton key={n.href} item={n} pathname={pathname} />
          ))}
        </NavSection>
      </aside>
    </TooltipProvider>
  );
}

function NavSection({
  label,
  className,
  children,
}: {
  label: string;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <div className={cn("flex w-full flex-col items-center gap-1", className)}>
      <span className="sr-only">{label}</span>
      {children}
    </div>
  );
}

function Divider() {
  return <span className="my-2 h-px w-6 bg-[var(--border-subtle)]" aria-hidden />;
}

function NavRailButton({ item, pathname }: { item: NavItem; pathname: string | null }) {
  const Icon = item.icon;
  const active = pathname === item.href || pathname?.startsWith(item.href + "/");
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Link
            href={item.href}
            aria-label={item.name}
            aria-current={active ? "page" : undefined}
            className={cn(
              "group inline-flex h-9 w-9 items-center justify-center rounded-md transition-colors",
              active
                ? "bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                : "text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
            )}
          >
            <Icon className="h-[15px] w-[15px]" />
          </Link>
        }
      />
      <TooltipContent side="right" sideOffset={8} className="text-xs">
        {item.name}
      </TooltipContent>
    </Tooltip>
  );
}
