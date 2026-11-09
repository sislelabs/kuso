"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, Circle } from "lucide-react";
import { useCan, Perms } from "@/features/auth";
import { listAlerts } from "@/features/alerts/api";
import type { ProjectSummaryItem } from "@/features/projects/api";
import { servicePageHref } from "@/components/service/workspace/hrefs";
import { api } from "@/lib/api-client";
import { cn } from "@/lib/utils";
import { nextSteps, type NextStepId } from "./progress";

// NextSteps is the welcome page for a team that already deploys: a short
// checklist of what usually comes next, ticked from live data.
export function NextSteps({ hasGitHub, projects }: { hasGitHub: boolean; projects: readonly ProjectSummaryItem[] }) {
  const canUsers = useCan(Perms.UserWrite);
  const users = useQuery({
    queryKey: ["admin", "users"],
    queryFn: async () => {
      const res = await api<{ data?: unknown[] } | unknown[]>("/api/users");
      return Array.isArray(res) ? res : (res.data ?? []);
    },
    enabled: canUsers,
  });
  const alerts = useQuery({ queryKey: ["alerts"], queryFn: listAlerts, retry: false, meta: { skipGlobalErrorToast: true } });
  const newest = [...projects].sort((a, b) =>
    (b.project.metadata.creationTimestamp ?? "").localeCompare(a.project.metadata.creationTimestamp ?? ""),
  )[0];
  const pName = newest?.project.metadata.name ?? "";
  const firstService = newest?.services[0]?.metadata.name.replace(`${pName}-`, "") ?? "";
  const steps = nextSteps({
    hasGitHub,
    environments: projects.flatMap((p) => p.environments.map((e) => e.spec)),
    userCount: canUsers && users.data ? users.data.length : null,
    alertRuleCount: alerts.data ? alerts.data.length : null,
  });

  const copy: Record<NextStepId, { title: string; body: string; href: string; cta: string }> = {
    github: {
      title: "Connect the GitHub App",
      body: "Without it, pushes don't build: every deploy is manual and PR previews are off.",
      href: "/settings/github",
      cta: "Connect GitHub",
    },
    database: {
      title: "Add a database",
      body: "Postgres, Redis and more run next to your services, with the connection string injected as variables.",
      href: pName ? `/projects/${encodeURIComponent(pName)}` : "/projects",
      cta: pName ? `Open ${pName}` : "Open a project",
    },
    domain: {
      title: "Connect a domain",
      body: "Serve a service on your own hostname. kuso shows the DNS record to create.",
      href: pName && firstService ? `${servicePageHref(pName, firstService, { tab: "settings" })}#domains` : "/projects",
      cta: "Add a domain",
    },
    teammate: {
      title: "Invite a teammate",
      body: "Give someone else access, with a role per project.",
      href: "/settings/users",
      cta: "Invite",
    },
    alerts: {
      title: "Set up alerts",
      body: "Get told when a service crashes or its error rate climbs, on Slack, Discord or email.",
      href: "/settings/alerts",
      cta: "Add an alert rule",
    },
  };

  const open = steps.filter((s) => !s.done).length;
  return (
    <section aria-labelledby="next-steps" className="space-y-3">
      <div>
        <h2 id="next-steps" className="text-base font-semibold tracking-tight">
          {open === 0 ? "You're all set" : "What's next"}
        </h2>
        <p className="mt-1 text-[13px] text-[var(--text-secondary)]">
          {open === 0
            ? "Your projects deploy, and the usual next steps are done."
            : `Your projects already run. ${open} ${open === 1 ? "step is" : "steps are"} worth doing next.`}
        </p>
      </div>
      <ul className="divide-y divide-[var(--border-subtle)] overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        {steps.map(({ id, done }) => {
          const c = copy[id];
          return (
            <li key={id} className="flex items-start gap-3 px-4 py-3">
              {done ? (
                <CheckCircle2 aria-hidden className="mt-0.5 h-4 w-4 shrink-0 text-[var(--success)]" />
              ) : (
                <Circle aria-hidden className="mt-0.5 h-4 w-4 shrink-0 text-[var(--text-tertiary)]" />
              )}
              <div className="min-w-0 flex-1">
                <p className={cn("text-sm font-medium", done ? "text-[var(--text-secondary)]" : "text-[var(--text-primary)]")}>
                  {c.title}
                  {done && <span className="sr-only"> (done)</span>}
                </p>
                {!done && <p className="mt-0.5 text-xs leading-relaxed text-[var(--text-secondary)]">{c.body}</p>}
              </div>
              {!done && (
                <Link
                  href={c.href}
                  className="shrink-0 text-xs font-medium text-[var(--text-primary)] underline underline-offset-2 hover:text-[var(--link)]"
                >
                  {c.cta}
                </Link>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
