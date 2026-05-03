"use client";

import Link from "next/link";
import { useCan, Perms } from "@/features/auth";
import {
  User as UserIcon,
  KeyRound,
  Bell,
  HardDrive,
  Package,
  Server,
  Settings as SettingsIcon,
  Users,
  UsersRound,
} from "lucide-react";
import { cn } from "@/lib/utils";

interface Card {
  href: string;
  title: string;
  description: string;
  icon: React.ComponentType<{ className?: string }>;
  perm?: string; // when set, hides the card without this permission
  group: "account" | "instance" | "admin";
}

// Settings index — every sub-page surfaces as a card. Gating per
// perm (admin-only sections drop off for project-scoped users
// instead of showing them and 403'ing on click).
const CARDS: Card[] = [
  // Account: anyone authed.
  { href: "/settings/profile",       title: "Profile",       description: "Name, email, password.",                                  icon: UserIcon,   group: "account" },
  { href: "/settings/tokens",        title: "API tokens",    description: "Mint personal access tokens for the CLI + scripts.",      icon: KeyRound,   group: "account" },
  { href: "/settings/notifications", title: "Notifications", description: "Discord webhooks, generic webhook fan-out, alerts.",      icon: Bell,       group: "account" },

  // Instance: shows for anyone, write-gated downstream.
  { href: "/settings/nodes",         title: "Cluster nodes",  description: "Tag nodes with labels for placement; schedulable state.", icon: Server,     group: "instance" },
  { href: "/settings/config",        title: "Cluster config", description: "Cluster-wide knobs (cert-manager email, base domain).",  icon: SettingsIcon, perm: Perms.SettingsRead, group: "instance" },
  { href: "/settings/backups",       title: "Backups",        description: "S3 credentials for scheduled addon dumps.",              icon: HardDrive,  perm: Perms.SettingsAdmin, group: "instance" },
  { href: "/settings/updates",       title: "Updates",        description: "Self-update the kuso server + operator + CRDs.",          icon: Package,    group: "instance" },

  // Admin: user/role management.
  { href: "/settings/users",  title: "Users",  description: "Local users. OAuth users land here on first login.",          icon: Users,      perm: Perms.UserWrite, group: "admin" },
  { href: "/settings/groups", title: "Groups", description: "Tenancy: instance roles + project memberships.",              icon: UsersRound, perm: Perms.UserWrite, group: "admin" },
];

const GROUPS: { id: Card["group"]; label: string; hint: string }[] = [
  { id: "account",  label: "Account",  hint: "your identity + how you receive notifications" },
  { id: "instance", label: "Instance", hint: "this kuso deployment" },
  { id: "admin",    label: "Admin",    hint: "user + access management" },
];

export default function SettingsIndex() {
  // We can't call useCan inside a render-time filter loop because
  // hooks must run unconditionally per render. Compute the allow
  // map up front with one hook call per card, in stable order.
  // Cards never reorder, so the rules-of-hooks invariant holds.
  const allow: boolean[] = CARDS.map((c) => useCanOrTrue(c.perm));

  return (
    <div className="mx-auto max-w-4xl p-6 lg:p-8">
      <header className="mb-8">
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Settings</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          Everything that isn&apos;t scoped to a single project. Some sections require admin
          permissions and won&apos;t show up if you don&apos;t have them.
        </p>
      </header>

      <div className="space-y-8">
        {GROUPS.map((g) => {
          const visible = CARDS.map((c, i) => ({ c, i }))
            .filter(({ c, i }) => c.group === g.id && allow[i]);
          if (visible.length === 0) return null;
          return (
            <section key={g.id}>
              <header className="mb-3 flex items-baseline justify-between">
                <h2 className="font-heading text-sm font-semibold tracking-tight">{g.label}</h2>
                <span className="font-mono text-[10px] text-[var(--text-tertiary)]">{g.hint}</span>
              </header>
              <ul className="grid gap-2 sm:grid-cols-2">
                {visible.map(({ c }) => (
                  <li key={c.href}>
                    <Link
                      href={c.href}
                      className={cn(
                        "group flex h-full items-start gap-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4",
                        "transition-colors hover:border-[var(--border-strong)] hover:bg-[var(--bg-tertiary)]/40"
                      )}
                    >
                      <span className="mt-0.5 inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] group-hover:text-[var(--text-primary)]">
                        <c.icon className="h-4 w-4" />
                      </span>
                      <div className="min-w-0">
                        <div className="text-sm font-semibold tracking-tight text-[var(--text-primary)]">
                          {c.title}
                        </div>
                        <p className="mt-0.5 text-[12px] text-[var(--text-secondary)]">
                          {c.description}
                        </p>
                      </div>
                    </Link>
                  </li>
                ))}
              </ul>
            </section>
          );
        })}
      </div>
    </div>
  );
}

// useCanOrTrue calls useCan with a sentinel when no perm is required
// so React's rules-of-hooks invariant (same hooks per render in the
// same order) holds regardless of the card's perm field.
function useCanOrTrue(perm?: string): boolean {
  const has = useCan(perm ?? Perms.SettingsRead);
  if (!perm) return true;
  return has;
}
