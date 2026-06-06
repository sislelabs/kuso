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
  Activity,
  ArrowDown,
  DollarSign,
  FileUp,
} from "lucide-react";
import { cn } from "@/lib/utils";

interface Card {
  href: string;
  title: string;
  description: string;
  icon: React.ComponentType<{ className?: string }>;
  perm?: string; // when set, locks the card without this permission
  // Group buckets settings by the operator-mental-model rather than
  // by who can see what. Cluster = state of this kuso deployment;
  // Team = users + groups + roles + audit; Integrations = external
  // wires (GitHub, Discord, Coolify import); You = personal scope.
  // This re-grouping replaces the old account/instance/admin split
  // because it scales better with 18+ routes (the old "instance"
  // bucket had become a 10-card dumping ground).
  group: "cluster" | "team" | "integrations" | "you";
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
  // You: personal-scope settings, anyone authed.
  { href: "/settings/profile",       title: "Profile",       description: "Name, email, password.",                                                  icon: UserIcon,     group: "you",  keywords: "name email password 2fa twofa avatar" },
  { href: "/settings/tokens",        title: "API tokens",    description: "Mint personal access tokens for the CLI + scripts.",                      icon: KeyRound,     group: "you",  keywords: "pat bearer cli script automation" },

  // Cluster: state of this kuso deployment.
  { href: "/settings/nodes",            title: "Cluster nodes",    description: "Tag nodes with labels for placement; schedulable state.",            icon: Server,       group: "cluster", keywords: "node labels placement cordon drain join bootstrap" },
  { href: "/settings/config",           title: "Cluster config",   description: "Cluster-wide knobs (cert-manager email, base domain).",              icon: SettingsIcon, perm: Perms.SettingsRead,  group: "cluster", keywords: "cert-manager letsencrypt domain dns base hostname" },
  { href: "/settings/database",         title: "Cluster database", description: "First-class Postgres. Managed on-cluster or external, plus extra registered servers — per-project databases.", icon: Database, perm: Perms.SettingsAdmin, group: "cluster", keywords: "postgres pg database shared cluster instance addons redis mysql clickhouse external neon rds supabase dsn" },
  { href: "/settings/instance-secrets", title: "Instance secrets", description: "Env vars auto-mounted into every service in every project.",        icon: Globe,        perm: Perms.SettingsAdmin, group: "cluster", keywords: "env environment variable global secret" },
  { href: "/settings/builds",           title: "Build resources",  description: "Concurrency cap + per-build memory/CPU limits. Tune to your VM size.", icon: Cpu,         perm: Perms.SettingsAdmin, group: "cluster", keywords: "kaniko buildpacks memory cpu limit concurrent" },
  { href: "/settings/backups",          title: "Backups",          description: "Server backup/restore + S3 credentials for scheduled addon dumps.", icon: HardDrive,    perm: Perms.SettingsAdmin, group: "cluster", keywords: "backup restore s3 dump pg_dump snapshot" },
  { href: "/settings/updates",          title: "Updates",          description: "Self-update the kuso server + operator + CRDs.",                    icon: Package,      perm: Perms.SettingsAdmin, group: "cluster", keywords: "version upgrade release self-update" },
  { href: "/settings/usage",            title: "Usage + cost",     description: "Per-node CPU + memory rollup with monthly cost projection.",        icon: DollarSign,   perm: Perms.SettingsRead,  group: "cluster", keywords: "cost spend billing dollars cpu memory rollup metrics estimate" },

  // Team: users + groups + roles + audit log.
  { href: "/settings/users",    title: "Users",    description: "Local users. OAuth users land here on first login.",                                  icon: Users,        perm: Perms.UserWrite, group: "team", keywords: "user account login oauth invite" },
  { href: "/settings/groups",   title: "Groups",   description: "Instance roles for groups; project access is granted per-project.",                  icon: UsersRound,   perm: Perms.UserWrite, group: "team", keywords: "group role permission tenancy member" },
  // /settings/activity is gated on audit:read, not settings:admin. The
  // page itself supports a non-admin "set the project filter" path
  // so users with audit:read can pull a project's audit log even
  // without instance-wide rights.
  { href: "/settings/activity", title: "Activity", description: "Audit log: who did what, when, against which project.",                               icon: Activity,     perm: Perms.AuditRead, group: "team", keywords: "audit log activity history who deleted changed" },

  // Integrations: external wires.
  { href: "/settings/github",        title: "GitHub App",   description: "Connect a GitHub App so kuso can monitor repos and trigger builds.", icon: Github,    perm: Perms.SettingsAdmin, group: "integrations", keywords: "github app webhook repo build push pr" },
  { href: "/settings/notifications", title: "Notifications", description: "Discord webhooks, generic webhook fan-out, alerts.",                  icon: Bell,      group: "integrations", keywords: "webhook slack discord email alert" },
  { href: "/settings/import",        title: "Import from Coolify", description: "Migrate Coolify apps + dbs + services into kuso.",             icon: ArrowDown, perm: Perms.SettingsAdmin, group: "integrations", keywords: "coolify import migrate migration" },
  { href: "/settings/import-compose", title: "Import docker-compose", description: "Convert a docker-compose.yml into kuso projects, services + addons.", icon: FileUp, perm: Perms.SettingsAdmin, group: "integrations", keywords: "docker compose import convert yaml migrate" },
];

const GROUPS: { id: Card["group"]; label: string; hint: string }[] = [
  { id: "cluster",      label: "Cluster",      hint: "this kuso deployment" },
  { id: "team",         label: "Team",         hint: "users + access + audit" },
  { id: "integrations", label: "Integrations", hint: "external services + notifications" },
  { id: "you",          label: "You",          hint: "personal settings" },
];

export default function SettingsIndex() {
  // We can't call useCan inside a render-time filter loop because
  // hooks must run unconditionally per render. Compute the allow
  // map up front with one hook call per card, in stable order.
  // CARDS is module-scoped + a constant — the rule-of-hooks
  // requirement (same number of hook calls per render) holds
  // because CARDS.length is fixed. ESLint can't prove that, so
  // suppress the rule on this line.
  // eslint-disable-next-line react-hooks/rules-of-hooks
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
            // Show every card in the group, but render gated ones in a
            // locked state so users who lack the perm still discover
            // the surface and know who to ask for access.
            const inGroup = CARDS.map((c, i) => ({ c, i, locked: !allow[i] })).filter(
              ({ c }) => c.group === g.id
            );
            if (inGroup.length === 0) return null;
            // Skip the section entirely when every card in it is
            // locked, except for the "team" section — keep it visible
            // (locked) so non-admins discover the surface and know
            // who to ask for access.
            const anyVisible = inGroup.some(({ locked }) => !locked);
            if (!anyVisible && g.id !== "team") return null;
            return (
              <section key={g.id}>
                <header className="mb-3 flex items-baseline justify-between">
                  <h2 className="font-heading text-sm font-semibold tracking-tight">{g.label}</h2>
                  <span className="font-mono text-[10px] text-[var(--text-tertiary)]">{g.hint}</span>
                </header>
                <ul className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
                  {inGroup.map(({ c, locked }) => (
                    <li key={c.href}>
                      <SettingsCard card={c} locked={locked} />
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

function SettingsCard({ card, locked = false }: { card: Card; locked?: boolean }) {
  const Icon = card.icon;
  if (locked) {
    return (
      <div
        title={card.perm ? `requires ${card.perm}` : "needs admin access"}
        className={cn(
          // Dead-opacity treatment replaced with a usable card that
          // still reads as "you can't enter this" but doesn't leave
          // viewers with no next step. Lighter opacity (0.85 vs 0.6)
          // keeps the text legible; the admin badge + hint line
          // tells them what to do.
          "flex h-full items-start gap-3 rounded-md border border-dashed border-[var(--border-subtle)] bg-[var(--bg-secondary)]/60 p-4 opacity-90"
        )}
      >
        <span className="mt-0.5 inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-[var(--bg-tertiary)] text-[var(--text-tertiary)]">
          <Icon className="h-4 w-4" />
        </span>
        <div className="min-w-0">
          <div className="flex items-center gap-1.5 text-sm font-semibold tracking-tight text-[var(--text-secondary)]">
            {card.title}
            <span className="rounded bg-[var(--bg-tertiary)] px-1 py-0.5 font-mono text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
              admin
            </span>
          </div>
          <p className="mt-0.5 text-[12px] text-[var(--text-tertiary)]">{card.description}</p>
          <p className="mt-1.5 font-mono text-[10px] text-[var(--text-tertiary)]/70">
            ask a team admin to enable it for you
          </p>
        </div>
      </div>
    );
  }
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
