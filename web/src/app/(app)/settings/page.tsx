"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
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
  Globe,
  Database,
  Github,
  Cpu,
  Search,
} from "lucide-react";
import { cn } from "@/lib/utils";

interface Card {
  href: string;
  title: string;
  description: string;
  icon: React.ComponentType<{ className?: string }>;
  perm?: string; // when set, hides the card without this permission
  group: "account" | "instance" | "admin";
  // keywords: words a user might type into the search box that aren't
  // in the title or description verbatim. e.g. typing "webhook" should
  // surface Notifications even though the description doesn't say
  // webhook in the cheap match. Lower-case, space-separated.
  keywords?: string;
}

// Settings index — every sub-page surfaces as a card. Gating per
// perm (admin-only sections drop off for project-scoped users
// instead of showing them and 403'ing on click).
const CARDS: Card[] = [
  // Account: anyone authed.
  { href: "/settings/profile",       title: "Profile",       description: "Name, email, password.",                                                  icon: UserIcon,     group: "account",  keywords: "name email password 2fa twofa avatar" },
  { href: "/settings/tokens",        title: "API tokens",    description: "Mint personal access tokens for the CLI + scripts.",                      icon: KeyRound,     group: "account",  keywords: "pat bearer cli script automation" },
  { href: "/settings/notifications", title: "Notifications", description: "Discord webhooks, generic webhook fan-out, alerts.",                      icon: Bell,         group: "account",  keywords: "webhook slack discord email alert" },

  // Instance: shows for anyone, write-gated downstream.
  { href: "/settings/nodes",            title: "Cluster nodes",    description: "Tag nodes with labels for placement; schedulable state.",            icon: Server,       group: "instance", keywords: "node labels placement cordon drain join bootstrap" },
  { href: "/settings/config",           title: "Cluster config",   description: "Cluster-wide knobs (cert-manager email, base domain).",              icon: SettingsIcon, perm: Perms.SettingsRead,  group: "instance", keywords: "cert-manager letsencrypt domain dns base hostname" },
  { href: "/settings/instance-secrets", title: "Instance secrets", description: "Env vars auto-mounted into every service in every project.",        icon: Globe,        perm: Perms.SettingsAdmin, group: "instance", keywords: "env environment variable global secret" },
  { href: "/settings/github",           title: "GitHub App",       description: "Connect a GitHub App so kuso can monitor repos and trigger builds.", icon: Github,       perm: Perms.SettingsAdmin, group: "instance", keywords: "github app webhook repo build push pr" },
  { href: "/settings/instance-addons",  title: "Instance addons",  description: "Shared databases that projects can carve per-project DBs out of.",  icon: Database,     perm: Perms.SettingsAdmin, group: "instance", keywords: "shared postgres redis mysql database" },
  { href: "/settings/builds",           title: "Build resources",  description: "Concurrency cap + per-build memory/CPU limits. Tune to your VM size.", icon: Cpu,         perm: Perms.SettingsAdmin, group: "instance", keywords: "kaniko buildpacks memory cpu limit concurrent" },
  { href: "/settings/backups",          title: "Backups",          description: "Server backup/restore + S3 credentials for scheduled addon dumps.", icon: HardDrive,    perm: Perms.SettingsAdmin, group: "instance", keywords: "backup restore s3 dump pg_dump snapshot" },
  { href: "/settings/updates",          title: "Updates",          description: "Self-update the kuso server + operator + CRDs.",                    icon: Package,      group: "instance", keywords: "version upgrade release self-update" },

  // Admin: user/role management.
  { href: "/settings/users",  title: "Users",  description: "Local users. OAuth users land here on first login.",                                    icon: Users,        perm: Perms.UserWrite, group: "admin", keywords: "user account login oauth invite" },
  { href: "/settings/groups", title: "Groups", description: "Tenancy: instance roles + project memberships.",                                        icon: UsersRound,   perm: Perms.UserWrite, group: "admin", keywords: "group role permission tenancy member" },
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
  const allow: boolean[] = CARDS.map((c) => useCanOrTrue(c.perm));

  const [query, setQuery] = useState("");
  const q = query.trim().toLowerCase();
  const matches = useMemo(() => {
    return CARDS.map((c, i) => {
      if (!allow[i]) return null;
      if (!q) return { c, i, score: 0 };
      const hay = `${c.title} ${c.description} ${c.keywords ?? ""}`.toLowerCase();
      // Cheap relevance: title-prefix beats description-contains beats
      // keyword-contains. Sub-string match throughout — this is an
      // index page, not a search engine.
      let score = 0;
      if (c.title.toLowerCase().startsWith(q)) score += 100;
      if (c.title.toLowerCase().includes(q)) score += 50;
      if (c.description.toLowerCase().includes(q)) score += 20;
      if ((c.keywords ?? "").includes(q)) score += 10;
      if (hay.includes(q)) score += 1;
      return score > 0 ? { c, i, score } : null;
    })
      .filter((x): x is { c: Card; i: number; score: number } => x !== null)
      .sort((a, b) => b.score - a.score);
  }, [q, allow]);

  return (
    <div className="mx-auto max-w-5xl p-6 lg:p-8">
      <header className="mb-6">
        <h1 className="font-heading text-2xl font-semibold tracking-tight">Settings</h1>
        <p className="mt-1 text-sm text-[var(--text-secondary)]">
          Everything that isn&apos;t scoped to a single project. Some sections require admin
          permissions and won&apos;t show up if you don&apos;t have them.
        </p>
      </header>

      {/* Search bar. Filters across title + description + keywords so
          "webhook" finds Notifications, "letsencrypt" finds Cluster
          config, etc. */}
      <div className="relative mb-8">
        <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-[var(--text-tertiary)]" />
        <input
          type="text"
          autoFocus
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search settings — try 'webhook', 'letsencrypt', 'pat'…"
          className="w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] py-2 pl-9 pr-3 text-sm text-[var(--text-primary)] placeholder:text-[var(--text-tertiary)] focus:border-[var(--border-strong)] focus:outline-none"
          aria-label="Search settings"
        />
        {q && (
          <button
            type="button"
            onClick={() => setQuery("")}
            className="absolute right-2 top-1/2 -translate-y-1/2 rounded px-2 py-0.5 font-mono text-[10px] text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            esc
          </button>
        )}
      </div>

      {q ? (
        // Search-results mode: flat list, no group headers, sorted by
        // relevance. Empty result shows a hint.
        <section>
          <header className="mb-3 flex items-baseline justify-between">
            <h2 className="font-heading text-sm font-semibold tracking-tight">
              {matches.length} result{matches.length === 1 ? "" : "s"}
            </h2>
            {matches.length === 0 && (
              <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
                nothing matches &quot;{query}&quot;
              </span>
            )}
          </header>
          <ul className="grid gap-2 sm:grid-cols-2">
            {matches.map(({ c }) => (
              <li key={c.href}>
                <SettingsCard card={c} />
              </li>
            ))}
          </ul>
        </section>
      ) : (
        // Browse mode: grouped sections.
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
                <ul className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
                  {visible.map(({ c }) => (
                    <li key={c.href}>
                      <SettingsCard card={c} />
                    </li>
                  ))}
                </ul>
              </section>
            );
          })}
        </div>
      )}
    </div>
  );
}

function SettingsCard({ card }: { card: Card }) {
  const Icon = card.icon;
  return (
    <Link
      href={card.href}
      className={cn(
        "group flex h-full items-start gap-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4",
        "transition-colors hover:border-[var(--border-strong)] hover:bg-[var(--bg-tertiary)]/40",
      )}
    >
      <span className="mt-0.5 inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--text-tertiary)] group-hover:text-[var(--text-primary)]">
        <Icon className="h-4 w-4" />
      </span>
      <div className="min-w-0">
        <div className="text-sm font-semibold tracking-tight text-[var(--text-primary)]">
          {card.title}
        </div>
        <p className="mt-0.5 text-[12px] text-[var(--text-secondary)]">{card.description}</p>
      </div>
    </Link>
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
