"use client";

import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { toast } from "sonner";
import { useDeleteProject } from "@/features/projects";
import { usePatchService, type PatchServiceBody } from "@/features/services";
import { useRouter } from "next/navigation";
import type { KusoService } from "@/types/projects";
import { Github, Trash2, Cog, Network, Layers3, Hammer, Cloud, Save, HardDrive, Plus, X } from "lucide-react";
import { cn } from "@/lib/utils";

interface Props {
  project: string;
  service: string;
  svc?: KusoService;
}

const SECTIONS: { id: string; label: string; icon: React.ComponentType<{ className?: string }> }[] = [
  { id: "source", label: "Source", icon: Github },
  { id: "networking", label: "Networking", icon: Network },
  { id: "scale", label: "Scale", icon: Layers3 },
  { id: "volumes", label: "Volumes", icon: HardDrive },
  { id: "build", label: "Build", icon: Hammer },
  { id: "deploy", label: "Deploy", icon: Cloud },
  { id: "danger", label: "Danger", icon: Trash2 },
];

const RUNTIMES = ["dockerfile", "nixpacks", "static", "buildpacks"] as const;

// ServiceSettingsPanel renders one tall column of sections with a
// right-rail anchor nav. Each section has its own form + Save — the
// right thing for k8s where every spec write triggers a reconcile.
// Saving Networking shouldn't churn Build's controller.
export function ServiceSettingsPanel({ project, service, svc }: Props) {
  return (
    <div className="grid grid-cols-[1fr_180px] gap-0">
      {/* Scrollable content column */}
      <div className="space-y-10 px-6 py-6">
        <SourceSection svc={svc} />
        <NetworkingSection project={project} service={service} svc={svc} />
        <ScaleSection project={project} service={service} svc={svc} />
        <VolumesSection project={project} service={service} svc={svc} />
        <BuildSection project={project} service={service} svc={svc} />
        <DeploySection svc={svc} />
        <DangerSection project={project} service={service} svc={svc} />
      </div>

      {/* Right rail anchor nav */}
      <nav className="sticky top-0 self-start px-4 py-6 text-sm">
        <ul className="space-y-2">
          {SECTIONS.map((s) => (
            <li key={s.id}>
              <a
                href={`#${s.id}`}
                className={cn(
                  "flex items-center gap-2 rounded px-2 py-1 text-[var(--text-tertiary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)] transition-colors",
                  s.id === "danger" && "text-red-400/70 hover:text-red-400"
                )}
              >
                <s.icon className="h-3 w-3" />
                {s.label}
              </a>
            </li>
          ))}
        </ul>
      </nav>
    </div>
  );
}

function Section({
  id,
  title,
  icon: Icon,
  children,
}: {
  id: string;
  title: string;
  icon: React.ComponentType<{ className?: string }>;
  children: React.ReactNode;
}) {
  return (
    <section id={id} className="scroll-mt-6">
      <div className="mb-3 flex items-center gap-2">
        <Icon className="h-4 w-4 text-[var(--text-tertiary)]" />
        <h3 className="font-heading text-base font-semibold tracking-tight">{title}</h3>
      </div>
      <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
        {children}
      </div>
    </section>
  );
}

function FieldRow({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1.5 sm:flex-row sm:items-baseline sm:gap-3">
      <div className="w-28 shrink-0">
        <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          {label}
        </div>
        {hint && <div className="mt-0.5 text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>}
      </div>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

function SaveBar({
  dirty,
  pending,
  onSave,
  onReset,
}: {
  dirty: boolean;
  pending: boolean;
  onSave: () => void;
  onReset: () => void;
}) {
  return (
    <div className="mt-3 flex items-center justify-end gap-2">
      {dirty && (
        <button
          type="button"
          onClick={onReset}
          className="font-mono text-[10px] text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]"
        >
          discard
        </button>
      )}
      <Button size="sm" disabled={!dirty || pending} onClick={onSave}>
        <Save className="h-3 w-3" />
        {pending ? "Saving…" : "Save"}
      </Button>
    </div>
  );
}

// ---- Sections ------------------------------------------------------

function SourceSection({ svc }: { svc?: KusoService }) {
  // Source is read-only for now — repo URL ties to a GitHub App
  // installation and changing it mid-life-cycle requires re-auth and
  // possibly a fresh project. Surface it but don't let users break it.
  return (
    <Section id="source" title="Source" icon={Github}>
      {svc?.spec.repo?.url ? (
        <div className="space-y-2">
          <FieldRow label="repository">
            <span className="font-mono text-[12px] text-[var(--text-secondary)]">
              {svc.spec.repo.url.replace(/^https?:\/\/(www\.)?/, "")}
            </span>
          </FieldRow>
          {svc.spec.repo.path && svc.spec.repo.path !== "." && (
            <FieldRow label="path">
              <span className="font-mono text-[12px] text-[var(--text-secondary)]">
                {svc.spec.repo.path}
              </span>
            </FieldRow>
          )}
          <p className="mt-2 text-[10px] text-[var(--text-tertiary)]">
            Repo + path are bound to the GitHub App installation. To change them, recreate
            the service.
          </p>
        </div>
      ) : (
        <p className="text-xs text-[var(--text-tertiary)]">No repo connected.</p>
      )}
    </Section>
  );
}

function NetworkingSection({ project, service, svc }: Props) {
  const [port, setPort] = useState<string>(String(svc?.spec.port ?? 8080));
  const [domains, setDomains] = useState<string>(
    (svc?.spec.domains ?? []).map((d) => d.host).join("\n")
  );
  const patch = usePatchService(project, service);

  useEffect(() => {
    setPort(String(svc?.spec.port ?? 8080));
    setDomains((svc?.spec.domains ?? []).map((d) => d.host).join("\n"));
  }, [svc]);

  const dirty =
    Number(port) !== (svc?.spec.port ?? 8080) ||
    domains !== (svc?.spec.domains ?? []).map((d) => d.host).join("\n");

  const onSave = async () => {
    const portNum = Number(port);
    if (!Number.isInteger(portNum) || portNum < 1 || portNum > 65535) {
      toast.error("Port must be 1–65535");
      return;
    }
    const hosts = domains
      .split("\n")
      .map((s) => s.trim())
      .filter(Boolean);
    const body: PatchServiceBody = {
      port: portNum,
      domains: hosts.map((h) => ({ host: h, tls: true })),
    };
    try {
      await patch.mutateAsync(body);
      toast.success("Networking saved");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save");
    }
  };

  return (
    <Section id="networking" title="Networking" icon={Network}>
      <div className="space-y-3">
        <FieldRow label="port" hint="container port">
          <Input
            type="number"
            value={port}
            onChange={(e) => setPort(e.target.value)}
            min={1}
            max={65535}
            className="h-8 w-32 font-mono text-[12px]"
          />
        </FieldRow>
        <FieldRow label="domains" hint="one per line; auto-TLS">
          <textarea
            value={domains}
            onChange={(e) => setDomains(e.target.value)}
            spellCheck={false}
            rows={3}
            placeholder="api.example.com"
            className="w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 font-mono text-[12px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
          />
          <p className="mt-1 text-[10px] text-[var(--text-tertiary)]">
            Leave empty to use the auto-generated host from the project base domain.
          </p>
        </FieldRow>
      </div>
      <SaveBar
        dirty={dirty}
        pending={patch.isPending}
        onSave={onSave}
        onReset={() => {
          setPort(String(svc?.spec.port ?? 8080));
          setDomains((svc?.spec.domains ?? []).map((d) => d.host).join("\n"));
        }}
      />
    </Section>
  );
}

function ScaleSection({ project, service, svc }: Props) {
  // Sleep is derived: min === 0 means "scale to zero when idle".
  // We collapsed the previous separate Sleep section because users
  // kept asking what the difference was — there isn't one. The
  // server still writes both spec.scale.min=0 AND spec.sleep.enabled=true
  // for backwards compat with the operator chart that reads sleep.
  const initial = {
    min: svc?.spec.scale?.min ?? 1,
    max: svc?.spec.scale?.max ?? 5,
    targetCPU: svc?.spec.scale?.targetCPU ?? 70,
  };
  const [s, setS] = useState(initial);
  const patch = usePatchService(project, service);

  useEffect(() => {
    setS({
      min: svc?.spec.scale?.min ?? 1,
      max: svc?.spec.scale?.max ?? 5,
      targetCPU: svc?.spec.scale?.targetCPU ?? 70,
    });
  }, [svc]);

  const dirty =
    s.min !== initial.min ||
    s.max !== initial.max ||
    s.targetCPU !== initial.targetCPU;

  const sleepsWhenIdle = s.min === 0;

  const onSave = async () => {
    if (s.min < 0 || s.max < Math.max(s.min, 1)) {
      toast.error("max must be ≥ max(min, 1) and min ≥ 0");
      return;
    }
    try {
      await patch.mutateAsync({
        scale: { min: s.min, max: s.max, targetCPU: s.targetCPU },
        // Mirror min=0 onto the sleep flag so the operator chart
        // that gates idle-scale-down on sleep.enabled keeps
        // working.
        sleep: { enabled: s.min === 0, afterMinutes: 5 },
      });
      toast.success("Scale saved");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save");
    }
  };

  return (
    <Section id="scale" title="Scale" icon={Layers3}>
      <div className="space-y-3">
        <FieldRow label="min replicas" hint="0 = sleep when idle">
          <Input
            type="number"
            value={s.min}
            onChange={(e) => setS((p) => ({ ...p, min: Number(e.target.value) }))}
            className="h-8 w-24 font-mono text-[12px]"
            min={0}
          />
        </FieldRow>
        <FieldRow label="max replicas" hint="ceiling for autoscale">
          <Input
            type="number"
            value={s.max}
            onChange={(e) => setS((p) => ({ ...p, max: Number(e.target.value) }))}
            className="h-8 w-24 font-mono text-[12px]"
            min={1}
          />
        </FieldRow>
        <FieldRow label="cpu threshold" hint="add a replica past this %">
          <div className="flex items-center gap-1.5">
            <Input
              type="number"
              value={s.targetCPU}
              onChange={(e) => setS((p) => ({ ...p, targetCPU: Number(e.target.value) }))}
              className="h-8 w-20 font-mono text-[12px]"
              min={1}
              max={100}
            />
            <span className="font-mono text-[11px] text-[var(--text-tertiary)]">%</span>
          </div>
        </FieldRow>
        <p
          className={cn(
            "rounded-md px-2 py-1.5 font-mono text-[10px]",
            sleepsWhenIdle
              ? "bg-amber-500/10 text-amber-400"
              : "bg-[var(--bg-tertiary)]/40 text-[var(--text-tertiary)]"
          )}
        >
          {sleepsWhenIdle ? (
            <>
              <span className="font-semibold">Sleeps when idle.</span> Pods scale to zero after a
              few minutes with no traffic; first request after wakes a fresh pod (~3s cold
              start).
            </>
          ) : (
            <>
              Always-on: at least <span className="text-[var(--text-secondary)]">{s.min}</span>{" "}
              {s.min === 1 ? "pod" : "pods"} kept warm. Set min to{" "}
              <span className="text-[var(--text-secondary)]">0</span> to enable sleep.
            </>
          )}
        </p>
      </div>
      <SaveBar
        dirty={dirty}
        pending={patch.isPending}
        onSave={onSave}
        onReset={() => setS(initial)}
      />
    </Section>
  );
}

function VolumesSection({ project, service, svc }: Props) {
  // Volumes are PVCs mounted into every pod. Each row needs a name +
  // mountPath; size defaults to 1Gi if not given. Names are stable
  // (used as PVC suffix), so renaming a volume == drop+recreate
  // which would lose data — we don't expose a rename, just add/del.
  type Row = { name: string; mountPath: string; sizeGi: number };
  const initial: Row[] = (svc?.spec.volumes ?? []).map((v) => ({
    name: v.name,
    mountPath: v.mountPath,
    sizeGi: v.sizeGi ?? 1,
  }));
  const [rows, setRows] = useState<Row[]>(initial);
  const patch = usePatchService(project, service);

  useEffect(() => {
    setRows(
      (svc?.spec.volumes ?? []).map((v) => ({
        name: v.name,
        mountPath: v.mountPath,
        sizeGi: v.sizeGi ?? 1,
      }))
    );
  }, [svc]);

  const dirty =
    JSON.stringify(rows.filter((r) => r.name && r.mountPath)) !==
    JSON.stringify(initial);

  const onSave = async () => {
    const cleaned = rows.filter((r) => r.name && r.mountPath);
    for (const r of cleaned) {
      if (!/^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$/.test(r.name)) {
        toast.error(`Volume name "${r.name}": lowercase, dashes, ≤32 chars`);
        return;
      }
      if (!r.mountPath.startsWith("/")) {
        toast.error(`Mount path "${r.mountPath}" must start with /`);
        return;
      }
    }
    try {
      await patch.mutateAsync({ volumes: cleaned });
      toast.success("Volumes saved");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save");
    }
  };

  return (
    <Section id="volumes" title="Volumes" icon={HardDrive}>
      <div className="space-y-2">
        {rows.length === 0 ? (
          <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-3 py-4 text-center text-[10px] text-[var(--text-tertiary)]">
            No persistent volumes. Add one for SQLite, file uploads, or any state that
            should survive pod restarts.
          </p>
        ) : (
          <ul className="space-y-1.5">
            {rows.map((r, i) => (
              <li key={i} className="grid grid-cols-[120px_1fr_72px_auto] items-center gap-1.5">
                <Input
                  value={r.name}
                  onChange={(e) =>
                    setRows((rs) => rs.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))
                  }
                  placeholder="data"
                  className="h-7 font-mono text-[11px]"
                />
                <Input
                  value={r.mountPath}
                  onChange={(e) =>
                    setRows((rs) => rs.map((x, j) => (j === i ? { ...x, mountPath: e.target.value } : x)))
                  }
                  placeholder="/var/lib/app"
                  className="h-7 font-mono text-[11px]"
                />
                <div className="relative">
                  <Input
                    type="number"
                    value={r.sizeGi}
                    onChange={(e) =>
                      setRows((rs) =>
                        rs.map((x, j) => (j === i ? { ...x, sizeGi: Math.max(1, Number(e.target.value) || 1) } : x))
                      )
                    }
                    min={1}
                    className="h-7 pr-6 font-mono text-[11px]"
                  />
                  <span className="pointer-events-none absolute right-1.5 top-1/2 -translate-y-1/2 font-mono text-[10px] text-[var(--text-tertiary)]">
                    Gi
                  </span>
                </div>
                <button
                  type="button"
                  onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}
                  aria-label="Remove volume"
                  className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400"
                >
                  <X className="h-3 w-3" />
                </button>
              </li>
            ))}
          </ul>
        )}
        <button
          type="button"
          onClick={() =>
            setRows((rs) => [...rs, { name: "", mountPath: "", sizeGi: 1 }])
          }
          className="inline-flex items-center gap-1 text-[10px] text-[var(--accent)] hover:underline"
        >
          <Plus className="h-3 w-3" /> add volume
        </button>
        <p className="text-[10px] text-[var(--text-tertiary)]">
          Renaming or deleting a volume drops its data. The PVC stays attached as long as
          the name doesn't change.
        </p>
      </div>
      <SaveBar
        dirty={dirty}
        pending={patch.isPending}
        onSave={onSave}
        onReset={() =>
          setRows(
            (svc?.spec.volumes ?? []).map((v) => ({
              name: v.name,
              mountPath: v.mountPath,
              sizeGi: v.sizeGi ?? 1,
            }))
          )
        }
      />
    </Section>
  );
}

function BuildSection({ project, service, svc }: Props) {
  const [runtime, setRuntime] = useState<string>(svc?.spec.runtime ?? "dockerfile");
  const patch = usePatchService(project, service);

  useEffect(() => {
    setRuntime(svc?.spec.runtime ?? "dockerfile");
  }, [svc]);

  const dirty = runtime !== (svc?.spec.runtime ?? "dockerfile");

  const onSave = async () => {
    try {
      await patch.mutateAsync({ runtime });
      toast.success("Build strategy saved");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save");
    }
  };

  return (
    <Section id="build" title="Build" icon={Hammer}>
      <FieldRow label="strategy" hint="how kuso builds the image">
        <div className="inline-flex flex-wrap gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-0.5">
          {RUNTIMES.map((r) => (
            <button
              key={r}
              type="button"
              onClick={() => setRuntime(r)}
              className={cn(
                "rounded px-2 py-1 font-mono text-[11px] transition-colors",
                runtime === r
                  ? "bg-[var(--bg-tertiary)] text-[var(--text-primary)]"
                  : "text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
              )}
            >
              {r}
            </button>
          ))}
        </div>
      </FieldRow>
      {runtime === "nixpacks" && (
        <p className="mt-2 text-[10px] text-[var(--text-tertiary)]">
          kuso runs <span className="font-mono">nixpacks build --out</span> in an init container, then
          kaniko ships the emitted Dockerfile.
        </p>
      )}
      {runtime === "static" && (
        <p className="mt-2 text-[10px] text-[var(--text-tertiary)]">
          Static sites: configure build cmd + output dir in the project YAML — UI fields land
          alongside the canvas-side spec editor.
        </p>
      )}
      <SaveBar
        dirty={dirty}
        pending={patch.isPending}
        onSave={onSave}
        onReset={() => setRuntime(svc?.spec.runtime ?? "dockerfile")}
      />
    </Section>
  );
}

function DeploySection({ svc }: { svc?: KusoService }) {
  void svc;
  // Deploy is informational — kuso always ships every successful build
  // to production, and previews are PR-driven. Toggling this off would
  // require a per-service flag in the spec we don't have yet.
  return (
    <Section id="deploy" title="Deploy" icon={Cloud}>
      <p className="text-xs text-[var(--text-secondary)]">
        kuso ships every successful build to <span className="font-mono">production</span>.
        Open a PR for an isolated preview environment.
      </p>
      <p className="mt-2 text-[10px] text-[var(--text-tertiary)]">
        Per-service auto-deploy gates land alongside the project-level deploy policy.
      </p>
    </Section>
  );
}

function DangerSection({ project, service, svc }: Props) {
  void svc;
  void service;
  const router = useRouter();
  const del = useDeleteProject();
  const [confirming, setConfirming] = useState(false);
  const [confirmText, setConfirmText] = useState("");

  const onDelete = async () => {
    if (confirmText !== service) {
      toast.error("Type the service name to confirm");
      return;
    }
    try {
      await del.mutateAsync(project);
      toast.success("Project deleted");
      router.replace("/projects");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to delete");
    }
  };

  return (
    <section id="danger" className="scroll-mt-6">
      <div className="mb-3 flex items-center gap-2">
        <Trash2 className="h-4 w-4 text-red-400" />
        <h3 className="font-heading text-base font-semibold tracking-tight text-red-400">
          Danger
        </h3>
      </div>
      <div className="rounded-md border border-red-500/30 bg-red-500/5 p-4">
        <h4 className="text-sm font-semibold">Delete project</h4>
        <p className="mt-1 text-xs text-[var(--text-secondary)]">
          Removes the project, every service, every preview env, and tears down the running
          pods. The git repo is untouched. This cannot be undone.
        </p>
        {!confirming ? (
          <Button variant="outline" size="sm" className="mt-3" onClick={() => setConfirming(true)}>
            <Trash2 className="h-3.5 w-3.5" /> Delete project
          </Button>
        ) : (
          <div className="mt-3 space-y-2">
            <Label htmlFor="confirm-del" className="text-xs">
              Type <span className="font-mono">{service}</span> to confirm
            </Label>
            <Input
              id="confirm-del"
              value={confirmText}
              onChange={(e) => setConfirmText(e.target.value)}
              className="font-mono text-sm"
              autoFocus
            />
            <div className="flex items-center gap-2">
              <Button
                variant="destructive"
                size="sm"
                onClick={onDelete}
                disabled={confirmText !== service || del.isPending}
              >
                {del.isPending ? "Deleting…" : "Confirm delete"}
              </Button>
              <Button variant="ghost" size="sm" onClick={() => { setConfirming(false); setConfirmText(""); }}>
                Cancel
              </Button>
            </div>
          </div>
        )}
      </div>
    </section>
  );
}

// Cog import kept so the import line above isn't pruned during refactor.
void Cog;
