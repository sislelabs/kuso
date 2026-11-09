"use client";

import { useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { X } from "lucide-react";
import { toast } from "sonner";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { SettingsSelect } from "@/components/settings/SettingsSection";
import {
  createAlert,
  updateAlert,
  testAlert,
  testSummary,
  ALERT_KINDS,
  DUR_UNITS,
  autoRuleName,
  buildCreateBody,
  emptyRuleForm,
  joinDur,
  splitDur,
  alertKindsFor,
  type AlertAccessSession,
  type AlertKind,
  type AlertRule,
  type AlertTestResult,
  type DurUnit,
  type RuleFormState,
} from "@/features/alerts";
import { toastError } from "@/lib/errors";
import { cn, serviceShortName } from "@/lib/utils";
import { useEnvironments, useServices } from "@/features/projects";
import { envGroupName } from "@/lib/env-group";

const KIND_GROUPS = ["Service", "Edge", "Logs", "Nodes"] as const;

// DurationInput is a whole number plus a unit, stored as "5m".
function DurationInput({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const { n, unit } = splitDur(value);
  return (
    <div className="flex gap-1.5">
      <Input
        type="number"
        min={1}
        inputMode="numeric"
        aria-label={`${label} amount`}
        value={n}
        onChange={(e) => onChange(joinDur(e.target.value.replace(/\D/g, ""), unit))}
        className="w-20 font-mono"
      />
      <SettingsSelect
        aria-label={`${label} unit`}
        value={unit}
        onChange={(e) => onChange(joinDur(n, e.target.value as DurUnit))}
      >
        {DUR_UNITS.map((u) => (
          <option key={u.value} value={u.value}>
            {u.label}
          </option>
        ))}
      </SettingsSelect>
    </div>
  );
}

// AlertRuleDialog is the create and edit form for one alert rule. The
// settings page opens it unscoped; a service's Alerts card opens it in
// place with `serviceScope`, which hides cluster-wide node kinds. With
// `editing` it saves over that rule (initial is ruleToForm(editing)).
export function AlertRuleDialog({
  initial,
  editing,
  access,
  projects,
  projectsLoading,
  onClose,
  onCreated,
  serviceScope = false,
}: {
  initial: RuleFormState;
  editing?: AlertRule;
  serviceScope?: boolean;
  access: AlertAccessSession;
  /** Projects the caller may scope a rule to. */
  projects: string[];
  projectsLoading: boolean;
  onClose: () => void;
  onCreated: () => void;
}) {
  const isAdmin = access.adminAll;
  const kinds = alertKindsFor(access).filter((k) => !serviceScope || ALERT_KINDS[k].scope !== "none");
  const [initialForm] = useState<RuleFormState>(() => {
    if (editing) return initial;
    // A deep link can name a project or kind this member can't use; drop
    // it rather than open a form that can only fail.
    if (isAdmin && !serviceScope) return initial;
    const kind = kinds.includes(initial.kind) ? initial.kind : "http_5xx_rate";
    if (isAdmin) return kind === initial.kind ? initial : { ...emptyRuleForm(kind), name: initial.name };
    const base = kind === initial.kind ? initial : { ...emptyRuleForm(kind), name: initial.name };
    // While the project list loads, keep the deep-linked scope: dropping it
    // here would open a service's "New alert" as an unscoped rule.
    return projectsLoading || projects.includes(base.project) ? base : { ...base, project: "", service: "", env: "" };
  });
  const [form, setFormRaw] = useState<RuleFormState>(initialForm);
  // The name follows kind and scope until the user edits it.
  const [nameAuto, setNameAuto] = useState(() => !editing && (!initialForm.name || initialForm.name === autoRuleName(initialForm)));
  const setForm = (update: (f: RuleFormState) => RuleFormState) =>
    setFormRaw((f) => {
      const next = update(f);
      return nameAuto ? { ...next, name: autoRuleName(next) } : next;
    });
  const dirty = JSON.stringify(form) !== JSON.stringify(initialForm);
  const meta = ALERT_KINDS[form.kind];
  const set = <K extends keyof RuleFormState>(k: K, v: RuleFormState[K]) => setForm((f) => ({ ...f, [k]: v }));
  const built = buildCreateBody(form, { projectRequired: !isAdmin });
  const services = useServices(form.project);
  const serviceNames = useMemo(
    () =>
      (services.data ?? [])
        .map((s) => serviceShortName(form.project, s.metadata.name))
        .sort((a, b) => a.localeCompare(b)),
    [services.data, form.project],
  );
  // Keep a prefilled value selectable even if the list doesn't have it
  // (stale deep link, list still loading).
  const projectOptions = form.project && !projects.includes(form.project) ? [form.project, ...projects] : projects;
  const serviceOptions =
    form.service && !serviceNames.includes(form.service) ? [form.service, ...serviceNames] : serviceNames;
  const envs = useEnvironments(form.project);
  // Env groups (production, staging, preview-pr-N…) of the picked service,
  // or of the whole project when the rule covers every service.
  const envNames = useMemo(() => {
    const fqn = form.service ? `${form.project}-${form.service}` : "";
    const names = (envs.data ?? []).filter((e) => !fqn || e.spec.service === fqn).map((e) => envGroupName(e));
    return [...new Set(names)].sort((a, b) => (a === "production" ? -1 : b === "production" ? 1 : a.localeCompare(b)));
  }, [envs.data, form.project, form.service]);
  const envOptions = form.env && !envNames.includes(form.env) ? [form.env, ...envNames] : envNames;
  const setProject = (project: string) => setForm((f) => ({ ...f, project, service: "", env: "" }));
  const noProjectReason = "Pick a project first";

  // A test sends the SAVED rule's card through every channel it reaches.
  const [testResult, setTestResult] = useState<AlertTestResult | null>(null);
  const [testing, setTesting] = useState(false);
  const sendTest = async (id: string) => {
    setTesting(true);
    try {
      const r = await testAlert(id);
      setTestResult(r);
      const sum = testSummary(r);
      if (sum.ok) toast.success("Test notification sent", { description: sum.text });
      else toast.error("Test notification didn't reach every channel", { description: sum.text });
    } catch (e) {
      toastError(e, "Couldn't send the test notification");
    } finally {
      setTesting(false);
    }
  };
  const create = useMutation({
    mutationFn: async ({ test }: { test: boolean }) => {
      if (!built.ok) throw new Error(built.error);
      const rule = editing ? await updateAlert(editing.id, built.body) : await createAlert(built.body);
      return { rule, test };
    },
    onSuccess: async ({ rule, test }) => {
      toast.success(editing ? `Saved ${form.name.trim()}` : `Alert ${form.name.trim()} created`);
      if (test && rule?.id) await sendTest(rule.id);
      onCreated();
    },
    onError: (e) => toastError(e, editing ? "Couldn't save the alert rule" : "Couldn't create the alert rule"),
  });
  const busy = create.isPending || testing;

  const changeKind = (kind: AlertKind) =>
    setForm((f) => ({ ...emptyRuleForm(kind), name: f.name, project: f.project, service: f.service, severity: f.severity }));

  return (
    <Dialog
      open
      onOpenChange={(next, details) => {
        if (next || busy) return;
        // A stray backdrop click shouldn't throw away a half-built rule.
        if (dirty && details.reason === "outside-press") return;
        onClose();
      }}
    >
      <DialogContent
        showCloseButton={false}
        className="max-h-[calc(100dvh-2rem)] max-w-lg gap-0 overflow-y-auto rounded-md bg-[var(--bg-secondary)] p-0 sm:max-w-lg"
      >
        <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-3">
          <div>
            <DialogTitle className="text-base font-semibold">{editing ? "Edit alert rule" : "New alert rule"}</DialogTitle>
            <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
              Evaluated every minute. Fires through your notification channels.
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            <X className="h-4 w-4" />
          </button>
        </header>
        <div className="space-y-4 p-4">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="Kind">
              <SettingsSelect
                value={form.kind}
                onChange={(e) => changeKind(e.target.value as AlertKind)}
              >
                {KIND_GROUPS.filter((g) => kinds.some((k) => ALERT_KINDS[k].group === g)).map((g) => (
                  <optgroup key={g} label={g}>
                    {kinds
                      .filter((k) => ALERT_KINDS[k].group === g)
                      .map((k) => (
                        <option key={k} value={k}>
                          {ALERT_KINDS[k].label}
                        </option>
                      ))}
                  </optgroup>
                ))}
              </SettingsSelect>
            </Field>
            <Field label="Name">
              <Input
                value={form.name}
                onChange={(e) => {
                  setNameAuto(false);
                  setFormRaw((f) => ({ ...f, name: e.target.value }));
                }}
              />
            </Field>
          </div>
          <p className="text-xs leading-relaxed text-[var(--text-secondary)]">{meta.help}</p>

          {form.kind === "log_match" && (
            <Field label="Query (substring)">
              <Input
                value={form.query}
                onChange={(e) => set("query", e.target.value)}
                placeholder="OOMKilled"
                className="font-mono"
              />
            </Field>
          )}

          {meta.scope !== "none" && (
            <div className={cn("grid grid-cols-1 gap-3", meta.scope === "env" ? "sm:grid-cols-3" : "sm:grid-cols-2")}>
              <Field
                label={isAdmin ? "Project" : "Project (required)"}
                hint={!isAdmin && projects.length === 0 && !projectsLoading ? "No project you can edit" : undefined}
              >
                <SettingsSelect
                  value={form.project}
                  onChange={(e) => setProject(e.target.value)}
                  placeholder={isAdmin ? undefined : projectsLoading ? "Loading projects…" : "Pick a project"}
                >
                  {isAdmin && <option value="">All projects</option>}
                  {projectOptions.map((p) => (
                    <option key={p} value={p}>
                      {p}
                    </option>
                  ))}
                </SettingsSelect>
              </Field>
              <Field
                label="Service"
                hint={
                  !form.project
                    ? noProjectReason
                    : services.isError
                      ? "Couldn't load services — the rule covers all of them"
                      : undefined
                }
              >
                <SettingsSelect
                  value={form.service}
                  onChange={(e) => set("service", e.target.value)}
                  disabled={!form.project || services.isError}
                  title={!form.project ? noProjectReason : undefined}
                >
                  <option value="">{services.isPending && form.project ? "Loading services…" : "All services"}</option>
                  {serviceOptions.map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </SettingsSelect>
              </Field>
              {meta.scope === "env" && (
                <Field label="Environment" hint={!form.project ? noProjectReason : undefined}>
                  <SettingsSelect
                    value={form.env}
                    onChange={(e) => set("env", e.target.value)}
                    disabled={!form.project}
                    title={!form.project ? noProjectReason : undefined}
                  >
                    <option value="">{envs.isPending && form.project ? "Loading environments…" : "All environments"}</option>
                    {envOptions.map((n) => (
                      <option key={n} value={n}>
                        {n}
                      </option>
                    ))}
                  </SettingsSelect>
                </Field>
              )}
            </div>
          )}

          {(meta.threshold || meta.minRequests) && (
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              {meta.threshold && (
                <Field label={meta.threshold.label}>
                  <Input
                    type="number"
                    value={form.threshold}
                    onChange={(e) => set("threshold", e.target.value)}
                    placeholder={meta.threshold.placeholder}
                    className="font-mono"
                  />
                </Field>
              )}
              {meta.minRequests && (
                <Field label="Min requests in window">
                  <Input
                    type="number"
                    value={form.minRequests}
                    onChange={(e) => set("minRequests", e.target.value)}
                    placeholder="20"
                    className="font-mono"
                  />
                </Field>
              )}
            </div>
          )}

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {meta.window && (
              <Field label="Window" hint="How far back each check looks">
                <DurationInput label="Window" value={form.window} onChange={(v) => set("window", v)} />
              </Field>
            )}
            <Field label="Throttle" hint="Minimum gap between two alerts">
              <DurationInput label="Throttle" value={form.throttle} onChange={(v) => set("throttle", v)} />
            </Field>
            <Field label="Severity">
              <SettingsSelect
                value={form.severity}
                onChange={(e) => set("severity", e.target.value as RuleFormState["severity"])}
              >
                <option value="info">Info</option>
                <option value="warn">Warning</option>
                <option value="error">Error</option>
              </SettingsSelect>
            </Field>
          </div>
        </div>
        <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
          {!built.ok && (
            <p id="alert-rule-blocked" role="status" className="mr-auto text-xs text-[var(--text-secondary)]">
              {built.error}
            </p>
          )}
          <Button size="sm" variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          {editing ? (
            <Button
              size="sm"
              variant="secondary"
              disabled={busy}
              onClick={() => void sendTest(editing.id)}
              title={dirty ? "Tests the saved rule; save first to test your changes" : "Sends this rule's alert to every channel it reaches"}
            >
              {testing ? "Sending…" : "Send test notification"}
            </Button>
          ) : (
            <Button
              size="sm"
              variant="secondary"
              disabled={!built.ok || busy}
              onClick={() => create.mutate({ test: true })}
              title="Creates the rule, then sends its alert to every channel it reaches"
            >
              {testing ? "Sending test…" : "Create & send test"}
            </Button>
          )}
          <Button
            size="sm"
            disabled={!built.ok || busy || (!!editing && !dirty)}
            aria-describedby={!built.ok ? "alert-rule-blocked" : undefined}
            onClick={() => create.mutate({ test: false })}
          >
            {create.isPending ? (editing ? "Saving…" : "Creating…") : editing ? "Save changes" : "Create rule"}
          </Button>
          {testResult && editing && (
            <ul role="status" className="w-full space-y-0.5 pt-1 text-xs">
              {testResult.results.length === 0 ? (
                <li className="text-[var(--text-secondary)]">{testSummary(testResult).text}</li>
              ) : (
                testResult.results.map((d) => (
                  <li key={d.channel} className={d.ok ? "text-[var(--text-secondary)]" : "text-[var(--error)]"}>
                    {d.name} ({d.type}): {d.ok ? "delivered" : (d.error ?? "failed")}
                  </li>
                ))
              )}
              {testResult.muted && (
                <li className="text-[var(--text-tertiary)]">The project is muted, so real warning and info alerts are held back.</li>
              )}
            </ul>
          )}
        </footer>
      </DialogContent>
    </Dialog>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="block text-sm font-medium text-[var(--text-primary)]">{label}</span>
      {children}
      {hint && <span className="block text-xs text-[var(--text-tertiary)]">{hint}</span>}
    </label>
  );
}
