"use client";

import { Select } from "@/components/ui/select";
import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { toast } from "sonner";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import {
  buildServiceEnvBody,
  createEnvironment,
  useAddons,
  useEnvGroups,
  type ServiceEnvForm,
} from "@/features/projects";
import { friendlyApiError } from "@/features/projects/names";
import { cn, serviceShortName } from "@/lib/utils";
import { isProductionResource } from "@/lib/env-group";

const STATEFUL_KINDS = ["postgres", "redis", "s3"];

// ServiceEnvironmentForm creates a long-lived environment for ONE service
// (POST /services/{s}/envs) — the per-service counterpart of the env-group
// mirror. By default the env gets its own copy of every stateful addon.
export function ServiceEnvironmentForm({
  project,
  services,
  onCancel,
  onCreated,
}: {
  project: string;
  services: { metadata: { name: string }; spec: { repo?: { defaultBranch?: string } } }[];
  onCancel: () => void;
  onCreated: (envName: string) => void;
}) {
  const qc = useQueryClient();
  const addons = useAddons(project);
  const groups = useEnvGroups(project);
  const shorts = services.map((s) => serviceShortName(project, s.metadata.name));
  const [service, setService] = useState(shorts[0] ?? "");
  const [form, setForm] = useState<ServiceEnvForm>({
    name: "",
    branch: "",
    host: "",
    shareAddons: false,
    seedFrom: "",
    addons: [],
  });
  const [error, setError] = useState<string | null>(null);

  // Stateful kinds the project actually has (clones carry an env label).
  const kinds = STATEFUL_KINDS.filter((k) =>
    (addons.data ?? []).some((a) => a.spec?.kind === k && isProductionResource(a.metadata.labels)),
  );
  const seedSources = (groups.data ?? []).filter((g) => g.kind !== "preview").map((g) => g.name);
  const defaultBranch =
    services.find((s) => serviceShortName(project, s.metadata.name) === service)?.spec.repo?.defaultBranch ?? "main";

  const create = useMutation({
    mutationFn: async () => {
      const body = buildServiceEnvBody(form);
      if ("error" in body) throw new Error(body.error);
      return createEnvironment(project, service, body);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["projects", project] });
      toast.success(
        `Environment "${form.name.trim()}" added to ${service}. Push to ${form.branch.trim()} or trigger a build to deploy it.`,
        { duration: 8000 },
      );
      onCreated(form.name.trim());
    },
    onError: (e) => setError(friendlyApiError(e, "Failed to create environment")),
  });

  const set = (patch: Partial<ServiceEnvForm>) => {
    setForm((f) => ({ ...f, ...patch }));
    setError(null);
  };
  const toggleKind = (k: string) =>
    set({ addons: form.addons.includes(k) ? form.addons.filter((x) => x !== k) : [...form.addons, k] });

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!create.isPending) create.mutate();
      }}
      className="flex min-h-0 flex-1 flex-col"
    >
      <div className="flex-1 space-y-3 overflow-y-auto p-4">
        <Label text="service">
          <div role="group" aria-label="Service" className="flex flex-wrap gap-1">
            {shorts.map((s) => (
              <Pill key={s} active={service === s} onClick={() => setService(s)}>
                {s}
              </Pill>
            ))}
          </div>
        </Label>
        <div className="grid grid-cols-2 gap-3">
          <Label text="name" htmlFor="svc-env-name">
            <Input
              id="svc-env-name"
              value={form.name}
              onChange={(e) => set({ name: e.target.value })}
              placeholder="staging"
              className="h-8 font-mono text-xs"
              spellCheck={false}
              autoFocus
            />
          </Label>
          <Label text="branch" htmlFor="svc-env-branch">
            <Input
              id="svc-env-branch"
              value={form.branch}
              onChange={(e) => set({ branch: e.target.value })}
              placeholder={defaultBranch}
              className="h-8 font-mono text-xs"
              spellCheck={false}
            />
          </Label>
        </div>
        <Label text="host" htmlFor="svc-env-host" hint="optional; default <service>-<env>.<project domain>">
          <Input
            id="svc-env-host"
            value={form.host}
            onChange={(e) => set({ host: e.target.value })}
            placeholder="staging.example.com"
            className="h-8 font-mono text-xs"
            spellCheck={false}
          />
        </Label>
        <div className="space-y-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-3 text-xs">
          <div role="group" aria-label="Addons" className="flex flex-wrap gap-1">
            <Pill active={!form.shareAddons} onClick={() => set({ shareAddons: false })}>
              own addons
            </Pill>
            <Pill active={form.shareAddons} onClick={() => set({ shareAddons: true })}>
              share production&apos;s
            </Pill>
          </div>
          {form.shareAddons ? (
            <p className="text-[var(--warning)]">
              This environment reads and writes production&apos;s databases, caches and buckets.
            </p>
          ) : (
            <>
              <p className="text-[var(--text-secondary)]">
                The environment gets its own copy of{" "}
                {form.addons.length === 0 ? "every stateful addon" : "the picked addons"}; its database
                starts empty unless you seed it.
              </p>
              {kinds.length > 1 && (
                <div className="flex flex-wrap items-center gap-2">
                  <span className="inline-block text-xs font-medium text-[var(--text-secondary)] first-letter:uppercase">
                    only
                  </span>
                  {kinds.map((k) => (
                    <label key={k} className="flex items-center gap-1 font-mono">
                      <input
                        type="checkbox"
                        checked={form.addons.includes(k)}
                        onChange={() => toggleKind(k)}
                        className="h-3 w-3 accent-[var(--accent)]"
                      />
                      {k}
                    </label>
                  ))}
                </div>
              )}
              {kinds.includes("postgres") && (
                <label className="flex items-center gap-2">
                  <span className="inline-block text-xs font-medium text-[var(--text-secondary)] first-letter:uppercase">
                    seed db from
                  </span>
                  <Select
                    size="sm"
                    value={form.seedFrom}
                    onChange={(e) => set({ seedFrom: e.target.value })}
                    className="font-mono"
                    wrapperClassName="w-auto"
                  >
                    <option value="">nothing (empty)</option>
                    {seedSources.map((g) => (
                      <option key={g} value={g}>
                        {g}
                      </option>
                    ))}
                  </Select>
                </label>
              )}
            </>
          )}
        </div>
        {error && (
          <p role="alert" className="text-xs text-[var(--error)]">
            {error}
          </p>
        )}
      </div>
      <div className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
        <Button type="button" variant="ghost" size="sm" onClick={onCancel} disabled={create.isPending}>
          Cancel
        </Button>
        <Button type="submit" size="sm" disabled={create.isPending || !service || !form.name.trim() || !form.branch.trim()}>
          <Plus className="h-3 w-3" />
          {create.isPending ? "Creating…" : "Create environment"}
        </Button>
      </div>
    </form>
  );
}

function Label({
  text,
  hint,
  htmlFor,
  children,
}: {
  text: string;
  hint?: string;
  htmlFor?: string;
  children: React.ReactNode;
}) {
  const cls = "block text-xs font-medium text-[var(--text-secondary)] first-letter:uppercase";
  return (
    <div className="space-y-1">
      {htmlFor ? (
        <label htmlFor={htmlFor} className={cls}>
          {text}
        </label>
      ) : (
        <div className={cls}>{text}</div>
      )}
      {children}
      {hint && <div className="text-xs text-[var(--text-tertiary)]">{hint}</div>}
    </div>
  );
}

function Pill({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "rounded-md border px-2 py-1 font-mono text-xs transition-colors",
        active
          ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)] text-[var(--text-primary)]"
          : "border-[var(--border-subtle)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)]",
      )}
    >
      {children}
    </button>
  );
}
