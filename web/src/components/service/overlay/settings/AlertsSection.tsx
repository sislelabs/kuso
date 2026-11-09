"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { BellRing, Plus } from "lucide-react";
import { ApiError } from "@/lib/api-client";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import {
  alertKindLabel,
  canEditAlertProject,
  defaultAlertKindFor,
  describeRule,
  listAlerts,
  serviceRuleForm,
  type AlertAccessSession,
  type AlertRule,
  type RuleFormState,
} from "@/features/alerts";
import { useSession } from "@/features/auth";
import type { KusoService } from "@/types/projects";
import { AlertRuleDialog } from "@/app/(app)/settings/alerts/AlertRuleDialog";
import { Section } from "./_primitives";

// rulesForService: rules that watch this service, either by name or by
// covering every service in its project.
export function rulesForService(rules: AlertRule[], project: string, service: string): AlertRule[] {
  return rules.filter((r) => r.project === project && (!r.service || r.service === service));
}

// AlertsSection lists the alert rules that cover this service, with a
// "New alert" button that opens the create form in place, scoped to it.
export function AlertsSection({
  project,
  service,
  env,
  svc,
}: {
  project: string;
  service: string;
  env: string;
  svc: KusoService | undefined;
}) {
  const qc = useQueryClient();
  const session = useSession();
  const access: AlertAccessSession = useMemo(
    () => ({
      adminAll: session.data?.session.adminAll ?? false,
      projectRoles: session.data?.session.projectRoles ?? {},
    }),
    [session.data],
  );
  const canAdd = canEditAlertProject(access, project);
  const [adding, setAdding] = useState<RuleFormState | null>(null);
  // Same cache entry as Settings → Alert rules.
  const q = useQuery({ queryKey: ["alerts"], queryFn: listAlerts, retry: false });
  const forbidden = q.error instanceof ApiError && (q.error.status === 403 || q.error.status === 404);
  const rules = q.data ? rulesForService(q.data, project, service) : [];
  const firing = rules.filter((r) => r.firingSince).length;

  return (
    <Section
      id="alerts"
      scope="none"
      title="Alerts"
      icon={BellRing}
      status={q.data ? (firing > 0 ? `${firing} firing` : `${rules.length} ${rules.length === 1 ? "rule" : "rules"}`) : undefined}
      description="Rules that notify you when this service misbehaves."
    >
      {q.isPending ? (
        <Skeleton className="h-10 w-full" />
      ) : forbidden ? (
        <p className="text-xs text-[var(--text-secondary)]">You don&apos;t have access to alert rules on this instance.</p>
      ) : q.isError ? (
        <p className="text-xs text-[var(--text-secondary)]">
          Couldn&apos;t load alert rules.{" "}
          <button type="button" onClick={() => void q.refetch()} className="underline underline-offset-2">
            Try again
          </button>
        </p>
      ) : rules.length === 0 ? (
        <p className="text-sm text-[var(--text-secondary)]">No alert rules cover {service} yet.</p>
      ) : (
        <ul className="divide-y divide-[var(--border-subtle)] rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)]">
          {rules.map((r) => (
            <li key={r.id} className="flex items-start gap-3 px-3 py-2.5">
              <span
                aria-hidden
                className={cn(
                  "mt-1.5 h-2 w-2 shrink-0 rounded-full",
                  r.firingSince ? "bg-[var(--error)]" : r.enabled ? "bg-[var(--success)]" : "bg-[var(--text-tertiary)]",
                )}
              />
              <div className="min-w-0 flex-1">
                <p className="flex flex-wrap items-baseline gap-x-2 text-sm">
                  <span className="font-medium text-[var(--text-primary)]">{r.name}</span>
                  <span className="text-xs text-[var(--text-tertiary)]">{alertKindLabel(r.kind)}</span>
                  {!r.service && <span className="text-xs text-[var(--text-tertiary)]">· all services in {project}</span>}
                </p>
                <p className="mt-0.5 break-words text-xs text-[var(--text-secondary)]">
                  {describeRule(r)}
                  {r.firingSince ? (
                    <span className="text-[var(--error)]"> · firing since {relativeTime(r.firingSince)}</span>
                  ) : !r.enabled ? (
                    " · off"
                  ) : r.lastFiredAt ? (
                    ` · last fired ${relativeTime(r.lastFiredAt)}`
                  ) : null}
                </p>
              </div>
            </li>
          ))}
        </ul>
      )}
      {!forbidden && (
        <div className="flex flex-wrap items-center gap-3">
          <Button
            type="button"
            variant="secondary"
            size="sm"
            disabled={!canAdd || session.isPending}
            disabledReason={canAdd || session.isPending ? undefined : `You need editor access on ${project} to add its alert rules`}
            onClick={() => setAdding(serviceRuleForm(defaultAlertKindFor(svc?.spec), project, service, env))}
          >
            <Plus aria-hidden className="h-3.5 w-3.5" />
            New alert rule
          </Button>
          <Link href="/settings/alerts" className="text-xs text-[var(--text-secondary)] underline-offset-2 hover:underline">
            All alert rules
          </Link>
        </div>
      )}
      {adding && (
        <AlertRuleDialog
          initial={adding}
          access={access}
          projects={canAdd ? [project] : []}
          projectsLoading={false}
          serviceScope
          onClose={() => setAdding(null)}
          onCreated={() => {
            setAdding(null);
            void qc.invalidateQueries({ queryKey: ["alerts"] });
          }}
        />
      )}
    </Section>
  );
}
