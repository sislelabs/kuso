import type { ComponentType } from "react";
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
  Activity,
  HeartPulse,
  ShieldAlert,
  ArrowDown,
  DollarSign,
  FileUp,
  Bot,
  Clock,
  Siren,
  Waves,
} from "lucide-react";
import { Perms } from "@/features/auth/hooks";

// Groups bucket settings by the operator's mental model rather than by
// who can see what: Cluster = state of this kuso deployment; Team =
// users + access + audit; Integrations = external wires; You = personal.
export type SettingsGroupId = "cluster" | "team" | "integrations" | "you";

export interface SettingsNavItem {
  href: string;
  title: string;
  description: string;
  icon: ComponentType<{ className?: string }>;
  // When set, the item is locked without this instance permission.
  perm?: string;
  group: SettingsGroupId;
  // Lower-case words a user might search for that aren't in the title
  // or description ("webhook" should surface Notifications).
  keywords?: string;
}

export const SETTINGS_GROUPS: { id: SettingsGroupId; label: string; hint: string }[] = [
  { id: "cluster", label: "Cluster", hint: "This kuso deployment" },
  { id: "team", label: "Team", hint: "Users, access and audit" },
  { id: "integrations", label: "Integrations", hint: "External services and notifications" },
  { id: "you", label: "Account", hint: "Your profile and tokens" },
];

export const SETTINGS_NAV: SettingsNavItem[] = [
  // Cluster
  { href: "/settings/config", title: "Cluster config", description: "Instance info, default pod size, presets, runpacks and the raw Kuso spec.", icon: SettingsIcon, perm: Perms.SettingsRead, group: "cluster", keywords: "cert-manager letsencrypt domain dns base hostname clusterissuer pod size runpack preset raw spec" },
  { href: "/settings/nodes", title: "Nodes", description: "Label nodes for placement, cordon, drain and join new ones.", icon: Server, perm: Perms.SettingsAdmin, group: "cluster", keywords: "node labels placement cordon drain join bootstrap server" },
  { href: "/settings/database", title: "Database", description: "Managed or external Postgres for per-project databases.", icon: Database, perm: Perms.SettingsAdmin, group: "cluster", keywords: "postgres pg database shared cluster instance addons external neon rds supabase dsn" },
  { href: "/settings/instance-secrets", title: "Instance secrets", description: "Env vars mounted into every service in every project.", icon: Globe, perm: Perms.SettingsAdmin, group: "cluster", keywords: "env environment variable global secret shared" },
  { href: "/settings/builds", title: "Builds", description: "Concurrency cap and per-build memory/CPU limits.", icon: Cpu, perm: Perms.SettingsAdmin, group: "cluster", keywords: "buildkit buildpacks memory cpu limit concurrent parallel" },
  { href: "/settings/sessions", title: "Sessions", description: "How long a login stays valid before re-auth.", icon: Clock, perm: Perms.SettingsAdmin, group: "cluster", keywords: "session login logout jwt token expiry timeout remember sign-out auth" },
  { href: "/settings/backups", title: "Backups", description: "S3 credentials for addon backups and control-plane backup health.", icon: HardDrive, perm: Perms.SettingsAdmin, group: "cluster", keywords: "backup restore s3 dump pg_dump snapshot bucket registry gc" },
  { href: "/settings/updates", title: "Updates", description: "Self-update the kuso server, operator and CRDs.", icon: Package, perm: Perms.SettingsAdmin, group: "cluster", keywords: "version upgrade release self-update changelog" },
  { href: "/settings/usage", title: "Usage & cost", description: "Per-node CPU and memory rollup with a monthly cost projection.", icon: DollarSign, perm: Perms.SettingsRead, group: "cluster", keywords: "cost spend billing dollars cpu memory rollup metrics estimate" },
  { href: "/settings/health", title: "Health", description: "Failed helm releases, stuck addons and orphaned envs, with fixes.", icon: HeartPulse, perm: Perms.SettingsRead, group: "cluster", keywords: "reconcile health helm operator stuck failed orphan drift remediate fix runbook" },
  { href: "/settings/vulnerabilities", title: "Vulnerabilities", description: "Trivy CVE scanning: deploy gate, nightly sweep, platform images.", icon: ShieldAlert, perm: Perms.SettingsAdmin, group: "cluster", keywords: "cve vulnerability security trivy scan image gate block enforce report" },

  // Team
  { href: "/settings/users", title: "Users", description: "Local and OAuth accounts, roles and invitations.", icon: Users, perm: Perms.UserWrite, group: "team", keywords: "user account login oauth invite invitation role" },
  { href: "/settings/groups", title: "Groups", description: "Instance roles for groups; project access is granted per project.", icon: UsersRound, perm: Perms.UserWrite, group: "team", keywords: "group role permission tenancy member" },
  // Gated on audit:read, not settings:admin: the page supports a
  // project-filter path for users without instance-wide rights.
  { href: "/settings/activity", title: "Activity", description: "Audit log: who did what, when, against which project.", icon: Activity, perm: Perms.AuditRead, group: "team", keywords: "audit log activity history who deleted changed" },

  // Integrations
  { href: "/settings/github", title: "GitHub App", description: "Connect a GitHub App to watch repos and trigger builds.", icon: Github, perm: Perms.SettingsAdmin, group: "integrations", keywords: "github app webhook repo build push pr oauth" },
  { href: "/settings/notifications", title: "Notifications", description: "Discord, Slack, Telegram, email and webhook channels.", icon: Bell, perm: Perms.SettingsAdmin, group: "integrations", keywords: "webhook slack discord email telegram pushover mattermost channel alert mute" },
  { href: "/settings/alerts", title: "Alert rules", description: "Rules evaluated every minute, fired through your channels.", icon: Siren, group: "integrations", keywords: "alert rule threshold cpu memory error rate page oncall" },
  { href: "/settings/drains", title: "Log & metrics drains", description: "Forward logs to Grafana, OTLP, Loki or a webhook.", icon: Waves, perm: Perms.SettingsAdmin, group: "integrations", keywords: "drain log forward otlp opentelemetry loki grafana datadog prometheus metrics scrape export observability" },
  { href: "/settings/incident-agent", title: "Incident agent", description: "An agent that investigates incidents and opens fix PRs.", icon: Bot, perm: Perms.SettingsAdmin, group: "integrations", keywords: "incident agent claude ai discord pr autonomous crash alert investigate" },
  { href: "/settings/import", title: "Import from Coolify", description: "Migrate Coolify apps, databases and services.", icon: ArrowDown, perm: Perms.SettingsAdmin, group: "integrations", keywords: "coolify import migrate migration" },
  { href: "/settings/import-compose", title: "Import docker-compose", description: "Convert a docker-compose.yml into projects, services and addons.", icon: FileUp, perm: Perms.SettingsAdmin, group: "integrations", keywords: "docker compose import convert yaml migrate" },

  // You
  { href: "/settings/profile", title: "Profile", description: "Name, email, avatar and password.", icon: UserIcon, group: "you", keywords: "name email password avatar account me" },
  { href: "/settings/tokens", title: "API tokens", description: "Personal access tokens for the CLI and scripts.", icon: KeyRound, group: "you", keywords: "pat bearer cli script automation token" },
];

// Cheap relevance: title prefix beats title contains beats description
// beats keywords. Returns 0 for no match. Substring matching only.
export function scoreSettingsItem(item: SettingsNavItem, query: string): number {
  const q = query.trim().toLowerCase();
  if (!q) return 1;
  const title = item.title.toLowerCase();
  let score = 0;
  if (title.startsWith(q)) score += 100;
  if (title.includes(q)) score += 50;
  if (item.description.toLowerCase().includes(q)) score += 20;
  if ((item.keywords ?? "").includes(q)) score += 10;
  return score;
}

// activeSettingsHref picks the nav entry owning a pathname. Longest
// prefix wins so /settings/import-compose doesn't light up Import.
export function activeSettingsHref(pathname: string, items: SettingsNavItem[] = SETTINGS_NAV): string | null {
  const clean = pathname.replace(/\/+$/, "");
  let best: string | null = null;
  for (const it of items) {
    if (clean === it.href || clean.startsWith(`${it.href}/`)) {
      if (!best || it.href.length > best.length) best = it.href;
    }
  }
  return best;
}
