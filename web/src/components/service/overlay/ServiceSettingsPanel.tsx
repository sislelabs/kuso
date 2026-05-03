"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { toast } from "sonner";
import { useDeleteProject } from "@/features/projects";
import { useRouter } from "next/navigation";
import type { KusoService } from "@/types/projects";
import { Github, Trash2, Cog, Network, Layers3, Hammer, Cloud } from "lucide-react";
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
  { id: "build", label: "Build", icon: Hammer },
  { id: "deploy", label: "Deploy", icon: Cloud },
  { id: "danger", label: "Danger", icon: Trash2 },
];

// ServiceSettingsPanel renders one tall column of sections with a
// right-rail anchor nav. Sections are read-only summaries first; in
// the future each gets an inline edit affordance. The panel is
// scoped to a single service; project-level settings (previews TTL,
// default branch, etc.) live on /projects/<p>/settings.
export function ServiceSettingsPanel({ project, service, svc }: Props) {
  return (
    <div className="grid grid-cols-[1fr_180px] gap-0">
      {/* Scrollable content column */}
      <div className="space-y-10 px-6 py-6">
        <Section id="source" title="Source" icon={Github}>
          {svc?.spec.repo?.url ? (
            <ReadRow label="repository">
              <span className="font-mono text-[var(--text-secondary)]">
                {svc.spec.repo.url.replace(/^https?:\/\/(www\.)?/, "")}
              </span>
            </ReadRow>
          ) : (
            <p className="text-xs text-[var(--text-tertiary)]">No repo connected.</p>
          )}
          {svc?.spec.repo?.path && svc.spec.repo.path !== "." && (
            <ReadRow label="path">
              <span className="font-mono text-[var(--text-secondary)]">{svc.spec.repo.path}</span>
            </ReadRow>
          )}
        </Section>

        <Section id="networking" title="Networking" icon={Network}>
          {svc?.spec.port !== undefined && (
            <ReadRow label="port">
              <span className="font-mono text-[var(--text-secondary)]">{svc.spec.port}</span>
            </ReadRow>
          )}
          {svc?.spec.domains?.length ? (
            <ReadRow label="domains">
              <ul className="space-y-1">
                {svc.spec.domains.map((d, i) => (
                  <li key={i} className="font-mono text-xs text-[var(--text-secondary)]">
                    {d.host}
                    {d.tls && (
                      <span className="ml-2 rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5 text-[9px] uppercase tracking-widest text-[var(--text-tertiary)]">
                        tls
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            </ReadRow>
          ) : (
            <p className="text-xs text-[var(--text-tertiary)]">
              Auto-generated host from project base domain. Add a custom domain via the API.
            </p>
          )}
        </Section>

        <Section id="scale" title="Scale" icon={Layers3}>
          <ReadRow label="replicas">
            <span className="font-mono text-[var(--text-secondary)]">
              {svc?.spec.scale?.min ?? 1}–{svc?.spec.scale?.max ?? 5}
            </span>
            <span className="ml-2 text-xs text-[var(--text-tertiary)]">
              autoscale on CPU {svc?.spec.scale?.targetCPU ?? 70}%
            </span>
          </ReadRow>
          {svc?.spec.sleep?.enabled && (
            <ReadRow label="sleep">
              <span className="text-xs text-[var(--text-secondary)]">
                Scale to zero after {svc.spec.sleep.afterMinutes ?? 30}m idle
              </span>
            </ReadRow>
          )}
        </Section>

        <Section id="build" title="Build" icon={Hammer}>
          <ReadRow label="strategy">
            <span className="font-mono text-[var(--text-secondary)]">
              {svc?.spec.runtime ?? "dockerfile"}
            </span>
          </ReadRow>
          {svc?.spec.runtime === "nixpacks" && (
            <p className="text-xs text-[var(--text-tertiary)]">
              kuso runs <span className="font-mono">nixpacks build --out</span> in an init container,
              then kaniko ships the emitted Dockerfile.
            </p>
          )}
          {svc?.spec.runtime === "static" && svc.spec.static && (
            <>
              {svc.spec.static.buildCmd && (
                <ReadRow label="build cmd">
                  <span className="font-mono text-xs text-[var(--text-secondary)]">
                    {svc.spec.static.buildCmd}
                  </span>
                </ReadRow>
              )}
              {svc.spec.static.outputDir && (
                <ReadRow label="output dir">
                  <span className="font-mono text-xs text-[var(--text-secondary)]">
                    {svc.spec.static.outputDir}
                  </span>
                </ReadRow>
              )}
            </>
          )}
        </Section>

        <Section id="deploy" title="Deploy" icon={Cloud}>
          <p className="text-xs text-[var(--text-tertiary)]">
            kuso ships every successful build to the production environment automatically. Push to
            the project's default branch to trigger a build, or open a PR for an isolated preview.
          </p>
        </Section>

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
      <div className="space-y-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
        {children}
      </div>
    </section>
  );
}

function ReadRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1 sm:flex-row sm:items-baseline sm:gap-3">
      <dt className="w-28 shrink-0 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
        {label}
      </dt>
      <dd className="min-w-0 flex-1 text-sm">{children}</dd>
    </div>
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
      // Service-level delete isn't a separate mutation yet — we drop
      // the project for now since most services are 1:1 with their
      // project. Per-service delete lands when the API surface gains
      // DELETE /api/projects/:p/services/:s parity in the React layer.
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
